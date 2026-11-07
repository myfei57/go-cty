package function

import (
	"errors"
	"fmt"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// CondFunc is the signature of a branch evaluation callback passed to
// [Cond]. It is called at most once and returns either the branch result
// or an error; it must not have any observable side effects that would
// be incorrect if the branch isn't selected, because [Cond] only invokes
// the branch (or branches) that its condition actually requires.
type CondFunc func() (cty.Value, error)

// CondBranch describes one of the two branches passed to [Cond].
type CondBranch struct {
	// ResultType is the type that the value returned from Evaluate must
	// conform to. The result types of the two branches must be safely
	// unifiable into a single common type, which is checked before either
	// branch is evaluated.
	ResultType cty.Type

	// Evaluate is called at most once to produce the branch result. Its
	// returned value must conform to ResultType; it is then safely
	// converted, if necessary, to the common type derived from the two
	// branches' result types.
	Evaluate CondFunc
}

// BranchError wraps an error that occurred while evaluating one of the
// branches passed to [Cond], identifying which branch was being evaluated
// so that callers can distinguish failures from the two different branches.
//
// When the condition is unknown and both branches fail, [Cond] returns a
// result of [errors.Join] wrapping one BranchError for each branch, with
// the true branch first and the false branch second.
type BranchError struct {
	error

	// WhenTrue is true if the failure came from the branch selected when
	// the condition is true, or false if it came from the branch selected
	// when the condition is false.
	WhenTrue bool
}

func branchName(whenTrue bool) string {
	if whenTrue {
		return "true"
	}
	return "false"
}

func branchError(whenTrue bool, err error) BranchError {
	return BranchError{
		error:    err,
		WhenTrue: whenTrue,
	}
}

func (e BranchError) Error() string {
	return fmt.Sprintf("%s conditional branch failed: %s", branchName(e.WhenTrue), e.error)
}

func (e BranchError) Unwrap() error {
	return e.error
}

// Cond lazily selects between the results of two branch callbacks based
// on the given boolean condition.
//
// This is similar to [cty.Value.Cond] except that the two candidate
// results are described by callbacks that produce them, so that branches
// that cannot be selected are never evaluated:
//
//   - If the condition is a known boolean then only the callback for the
//     selected branch is invoked, exactly once. The other callback is not
//     invoked at all, and so it is safe for it to panic, return an error,
//     or perform expensive work that would be incorrect to run when that
//     branch isn't selected.
//   - If the condition is unknown, including when it is [cty.DynamicVal],
//     then the true branch is evaluated first and the false branch second,
//     each exactly once, and the result is a conservative placeholder
//     derived with [cty.UnknownChoice] from the two results. If the two
//     results are identical then the result may remain known; otherwise
//     only the common non-nullness, lengths, and numeric ranges that can
//     be proven for both results are retained.
//
// A null condition is an error. A condition of any type other than
// [cty.Bool] and [cty.DynamicPseudoType] is an error.
//
// Before any branch is evaluated, the two declared result types must be
// safely unifiable into a common type (using [convert.Unify]). If they
// cannot be unified then an error is returned without either callback
// being invoked. Each branch's returned value must conform to its declared
// ResultType and is then safely converted to the common type.
//
// Marks on the condition are propagated to the result. Marks from the
// selected branch, including marks nested within its value, are preserved.
// When the condition is known, marks from the branch that wasn't selected
// cannot appear in the result because that branch is never evaluated. When
// the condition is unknown, the result carries the union of the marks from
// both branches, following the same rules as [cty.Value.Cond].
//
// A panic from a branch callback is captured and returned as a
// [PanicError] wrapped in a [BranchError], following the same convention
// as panics from function implementations, so that Go panics never escape
// this function.
func Cond(condition cty.Value, whenTrue, whenFalse CondBranch) (result cty.Value, err error) {
	// Validate the static branch descriptions before we do anything that
	// could potentially invoke either callback, so that misconfiguration is
	// always reported without evaluating anything.
	for _, branch := range []struct {
		name   string
		branch CondBranch
	}{
		{"true", whenTrue},
		{"false", whenFalse},
	} {
		if branch.branch.ResultType == cty.NilType {
			return cty.NilVal, fmt.Errorf("the %s conditional branch has no declared result type", branch.name)
		}
		if branch.branch.Evaluate == nil {
			return cty.NilVal, fmt.Errorf("the %s conditional branch has no evaluation function", branch.name)
		}
	}

	// The declared types must be safely unifiable before any branch can
	// run, so that we can guarantee that whichever branch is selected will
	// produce a value that can be represented in the common result type.
	commonType, convs := convert.Unify([]cty.Type{whenTrue.ResultType, whenFalse.ResultType})
	if commonType == cty.NilType {
		return cty.NilVal, fmt.Errorf(
			"conditional branch result types cannot be safely unified: %s for the true branch and %s for the false branch",
			whenTrue.ResultType.FriendlyName(), whenFalse.ResultType.FriendlyName(),
		)
	}
	trueConv := convs[0]
	falseConv := convs[1]

	if condition == cty.NilVal {
		return cty.NilVal, fmt.Errorf("conditional predicate must be Bool value")
	}

	// Condition marks are propagated to the result regardless of which
	// path we take below.
	var condMarks cty.ValueMarks
	condition, condMarks = condition.Unmark()

	switch {
	case condition.IsNull():
		return cty.NilVal, fmt.Errorf("cannot use null value as conditional predicate")
	case condition.Type() != cty.Bool && condition.Type() != cty.DynamicPseudoType:
		return cty.NilVal, fmt.Errorf("conditional predicate must be Bool value")
	}

	if condition.IsKnown() {
		// Only the selected branch is evaluated; the other branch's
		// callback is never invoked.
		if condition.True() {
			result, err = evalCondBranch(true, whenTrue, trueConv)
		} else {
			result, err = evalCondBranch(false, whenFalse, falseConv)
		}
		if err != nil {
			return cty.NilVal, err
		}
		return result.WithMarks(condMarks), nil
	}

	// The condition is unknown, so we don't yet know which branch will be
	// selected. We evaluate both, in a defined order, and combine the
	// results conservatively. Both branches are always evaluated even if
	// the first fails, so that we can report failures from both at once.
	trueVal, trueErr := evalCondBranch(true, whenTrue, trueConv)
	falseVal, falseErr := evalCondBranch(false, whenFalse, falseConv)
	if trueErr != nil || falseErr != nil {
		return cty.NilVal, errors.Join(trueErr, falseErr)
	}

	result = cty.UnknownChoice(func(yield func(cty.Value) bool) {
		if !yield(trueVal) {
			return
		}
		yield(falseVal)
	}, false)
	return result.WithMarks(condMarks), nil
}

// evalCondBranch invokes a single branch callback exactly once, checks
// that its result conforms to the declared type, converts it to the
// common result type, and wraps any failure (including a panic from the
// callback) in a BranchError.
func evalCondBranch(whenTrue bool, branch CondBranch, conv convert.Conversion) (result cty.Value, err error) {
	defer func() {
		if r := recover(); r != nil {
			result = cty.NilVal
			err = branchError(whenTrue, errorForPanic(r))
		}
	}()

	val, callbackErr := branch.Evaluate()
	if callbackErr != nil {
		return cty.NilVal, branchError(whenTrue, callbackErr)
	}
	if val == cty.NilVal {
		return cty.NilVal, branchError(whenTrue, fmt.Errorf("branch returned no value"))
	}
	if errs := val.Type().TestConformance(branch.ResultType); errs != nil {
		return cty.NilVal, branchError(whenTrue, fmt.Errorf(
			"returned value does not conform to declared result type %s: %s",
			branch.ResultType, errs[0],
		))
	}
	if conv != nil {
		converted, convErr := conv(val)
		if convErr != nil {
			return cty.NilVal, branchError(whenTrue, fmt.Errorf(
				"could not convert returned value to common conditional result type: %w",
				convErr,
			))
		}
		val = converted
	}
	return val, nil
}
