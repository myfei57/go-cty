package function

import (
	"errors"
	"math/big"
	"slices"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/ctymarks"
)

// testProvMark is a mark type that embeds [ctymarks.ProvenanceMark], so cty
// treats it conservatively and preserves it in nested positions of unknown
// results.
type testProvMark struct {
	ctymarks.ProvenanceMark
	name string
}

// condRecorder records how many times each branch callback runs and in what
// order the callbacks were invoked.
type condRecorder struct {
	trueCalls  int
	falseCalls int
	order      []string
}

func (r *condRecorder) branch(whenTrue bool, ty cty.Type, fn CondFunc) CondBranch {
	name := "false"
	if whenTrue {
		name = "true"
	}
	return CondBranch{
		ResultType: ty,
		Evaluate: func() (cty.Value, error) {
			if whenTrue {
				r.trueCalls++
			} else {
				r.falseCalls++
			}
			r.order = append(r.order, name)
			return fn()
		},
	}
}

func (r *condRecorder) checks(t *testing.T, wantTrue, wantFalse int, wantOrder ...string) {
	t.Helper()
	if r.trueCalls != wantTrue {
		t.Errorf("true branch ran %d times; want %d", r.trueCalls, wantTrue)
	}
	if r.falseCalls != wantFalse {
		t.Errorf("false branch ran %d times; want %d", r.falseCalls, wantFalse)
	}
	gotOrder := strings.Join(r.order, ",")
	wantOrderJoined := strings.Join(wantOrder, ",")
	if gotOrder != wantOrderJoined {
		t.Errorf("callback order was [%s]; want [%s]", gotOrder, wantOrderJoined)
	}
}

func panicBranch(msg string) CondFunc {
	return func() (cty.Value, error) {
		panic(msg)
	}
}

func valueBranch(v cty.Value) CondFunc {
	return func() (cty.Value, error) {
		return v, nil
	}
}

func errorBranch(msg string) CondFunc {
	return func() (cty.Value, error) {
		return cty.NilVal, errors.New(msg)
	}
}

// extractBranchErrors extracts all [BranchError] values from err, including
// errors joined together with [errors.Join].
func extractBranchErrors(t *testing.T, err error) []BranchError {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error but got nil")
	}
	var ret []BranchError
	var joined interface{ Unwrap() []error }
	if errors.As(err, &joined) {
		for _, sub := range joined.Unwrap() {
			var be BranchError
			if errors.As(sub, &be) {
				ret = append(ret, be)
			}
		}
	} else {
		var be BranchError
		if errors.As(err, &be) {
			ret = append(ret, be)
		}
	}
	if len(ret) == 0 {
		t.Fatalf("error %q does not wrap any BranchError", err)
	}
	return ret
}

func TestCondKnownSelectsOnlyChosenBranch(t *testing.T) {
	t.Run("true", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.True,
			rec.branch(true, cty.String, valueBranch(cty.StringVal("yes"))),
			rec.branch(false, cty.String, panicBranch("false branch must not run")),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.RawEquals(cty.StringVal("yes")) {
			t.Fatalf("wrong result %#v", got)
		}
		rec.checks(t, 1, 0, "true")
	})

	t.Run("false", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.False,
			rec.branch(true, cty.String, panicBranch("true branch must not run")),
			rec.branch(false, cty.String, valueBranch(cty.StringVal("no"))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.RawEquals(cty.StringVal("no")) {
			t.Fatalf("wrong result %#v", got)
		}
		rec.checks(t, 0, 1, "false")
	})

	t.Run("true with erroring unselected branch", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.True,
			rec.branch(true, cty.String, valueBranch(cty.StringVal("yes"))),
			rec.branch(false, cty.String, errorBranch("false branch is broken")),
		)
		if err != nil {
			t.Fatalf("unselected branch error leaked: %s", err)
		}
		rec.checks(t, 1, 0, "true")
	})

	t.Run("marked known condition", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.True.Mark("?"),
			rec.branch(true, cty.String, valueBranch(cty.StringVal("yes").Mark("y"))),
			rec.branch(false, cty.String, valueBranch(cty.StringVal("no").Mark("n"))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := cty.StringVal("yes").Mark("?").Mark("y")
		if !got.RawEquals(want) {
			t.Fatalf("wrong result %#v; want %#v", got, want)
		}
		rec.checks(t, 1, 0, "true")
	})
}

func TestCondUnknownEvaluatesBothInOrder(t *testing.T) {
	var rec condRecorder
	got, err := Cond(
		cty.UnknownVal(cty.Bool),
		rec.branch(true, cty.String, valueBranch(cty.StringVal("yes"))),
		rec.branch(false, cty.String, valueBranch(cty.StringVal("no"))),
	)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	want := cty.UnknownVal(cty.String).RefineNotNull()
	if !got.RawEquals(want) {
		t.Fatalf("wrong result %#v; want %#v", got, want)
	}
	rec.checks(t, 1, 1, "true", "false")
}

func TestCondDynamicValTreatedAsUnknownBool(t *testing.T) {
	var rec condRecorder
	got, err := Cond(
		cty.DynamicVal,
		rec.branch(true, cty.String, valueBranch(cty.StringVal("yes"))),
		rec.branch(false, cty.String, valueBranch(cty.StringVal("no"))),
	)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	want := cty.UnknownVal(cty.String).RefineNotNull()
	if !got.RawEquals(want) {
		t.Fatalf("wrong result %#v; want %#v", got, want)
	}
	rec.checks(t, 1, 1, "true", "false")
}

func TestCondInvalidCondition(t *testing.T) {
	t.Run("null bool", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.NullVal(cty.Bool),
			rec.branch(true, cty.String, panicBranch("must not run")),
			rec.branch(false, cty.String, panicBranch("must not run")),
		)
		if err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("want null-condition error, got %v", err)
		}
		rec.checks(t, 0, 0)
	})

	t.Run("null dynamic", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.NullVal(cty.DynamicPseudoType),
			rec.branch(true, cty.String, panicBranch("must not run")),
			rec.branch(false, cty.String, panicBranch("must not run")),
		)
		if err == nil || !strings.Contains(err.Error(), "null") {
			t.Fatalf("want null-condition error, got %v", err)
		}
		rec.checks(t, 0, 0)
	})

	t.Run("wrong type", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.StringVal("yes"),
			rec.branch(true, cty.String, panicBranch("must not run")),
			rec.branch(false, cty.String, panicBranch("must not run")),
		)
		if err == nil || !strings.Contains(err.Error(), "Bool") {
			t.Fatalf("want bool-condition error, got %v", err)
		}
		rec.checks(t, 0, 0)
	})
}

func TestCondDeclaredTypesMustUnifyBeforeBranchesRun(t *testing.T) {
	// Number and Bool cannot be safely unified (only unsafe string
	// conversions connect them), so this must fail before either branch
	// runs even though the condition is known.
	t.Run("known condition", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.True,
			rec.branch(true, cty.Number, panicBranch("true branch must not run")),
			rec.branch(false, cty.Bool, panicBranch("false branch must not run")),
		)
		if err == nil {
			t.Fatal("expected type-unification error")
		}
		rec.checks(t, 0, 0)
	})

	t.Run("unknown condition", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.Number, panicBranch("true branch must not run")),
			rec.branch(false, cty.Bool, panicBranch("false branch must not run")),
		)
		if err == nil {
			t.Fatal("expected type-unification error")
		}
		rec.checks(t, 0, 0)
	})

	t.Run("missing type and callback", func(t *testing.T) {
		_, err := Cond(
			cty.True,
			CondBranch{ResultType: cty.NilType, Evaluate: panicBranch("")},
			CondBranch{ResultType: cty.String, Evaluate: valueBranch(cty.StringVal("no"))},
		)
		if err == nil {
			t.Fatal("expected missing-type error")
		}
		_, err = Cond(
			cty.True,
			CondBranch{ResultType: cty.String, Evaluate: nil},
			CondBranch{ResultType: cty.String, Evaluate: valueBranch(cty.StringVal("no"))},
		)
		if err == nil {
			t.Fatal("expected missing-callback error")
		}
	})
}

func TestCondBranchResultMustConformToDeclaredType(t *testing.T) {
	t.Run("known condition", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.True,
			rec.branch(true, cty.Number, valueBranch(cty.StringVal("not a number"))),
			rec.branch(false, cty.Number, valueBranch(cty.Zero)),
		)
		branches := extractBranchErrors(t, err)
		if len(branches) != 1 || !branches[0].WhenTrue {
			t.Fatalf("want one true-branch error, got %#v", branches)
		}
		if !strings.Contains(branches[0].Error(), "declared result type") {
			t.Fatalf("want conformance error, got %s", branches[0])
		}
		rec.checks(t, 1, 0, "true")
	})

	t.Run("unknown condition, only false branch non-conformant", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.Number, valueBranch(cty.Zero)),
			rec.branch(false, cty.Number, valueBranch(cty.StringVal("not a number"))),
		)
		branches := extractBranchErrors(t, err)
		if len(branches) != 1 || branches[0].WhenTrue {
			t.Fatalf("want one false-branch error, got %#v", branches)
		}
		rec.checks(t, 1, 1, "true", "false")
	})
}

func TestCondBothUnknownBranchesFail(t *testing.T) {
	t.Run("both return errors", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, errorBranch("true is broken")),
			rec.branch(false, cty.String, errorBranch("false is broken")),
		)
		branches := extractBranchErrors(t, err)
		if len(branches) != 2 {
			t.Fatalf("want two branch errors, got %d", len(branches))
		}
		if !branches[0].WhenTrue || branches[1].WhenTrue {
			t.Fatalf("want true then false branch identity, got %#v", branches)
		}
		if !strings.Contains(branches[0].Error(), "true is broken") ||
			!strings.Contains(branches[1].Error(), "false is broken") {
			t.Fatalf("both original errors must be preserved: %s", err)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("one panic and one error, both preserved", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, panicBranch("true panicked")),
			rec.branch(false, cty.String, errorBranch("false errored")),
		)
		branches := extractBranchErrors(t, err)
		if len(branches) != 2 {
			t.Fatalf("want two branch errors, got %d", len(branches))
		}
		var pe PanicError
		if !errors.As(branches[0], &pe) || pe.Value != "true panicked" {
			t.Fatalf("true branch panic not wrapped as PanicError: %s", branches[0])
		}
		rec.checks(t, 1, 1, "true", "false")
	})
}

func TestCondPanicIsWrapped(t *testing.T) {
	t.Run("known condition, selected branch panics", func(t *testing.T) {
		var rec condRecorder
		_, err := Cond(
			cty.False,
			rec.branch(true, cty.String, panicBranch("true must not run")),
			rec.branch(false, cty.String, panicBranch("boom from false")),
		)
		branches := extractBranchErrors(t, err)
		if len(branches) != 1 || branches[0].WhenTrue {
			t.Fatalf("want one false-branch error, got %#v", branches)
		}
		var pe PanicError
		if !errors.As(err, &pe) {
			t.Fatalf("want PanicError in error chain, got %T %[1]v", err)
		}
		if pe.Value != "boom from false" {
			t.Fatalf("wrong panic value %#v", pe.Value)
		}
		rec.checks(t, 0, 1, "false")
	})
}

func TestCondBigIntegerSafeUnification(t *testing.T) {
	big1 := cty.MustParseNumberVal("1234567890123456789012345678901234567890")
	big2 := cty.MustParseNumberVal("9999999999999999999999999999999999999999")

	t.Run("known result preserves exact precision", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.True,
			rec.branch(true, cty.Number, valueBranch(big1)),
			rec.branch(false, cty.Number, panicBranch("false must not run")),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.RawEquals(big1) {
			t.Fatalf("big integer was not preserved exactly: %#v", got)
		}
		bi, acc := got.AsBigFloat().Int(nil)
		if acc != big.Exact {
			t.Fatalf("big integer conversion was not exact (acc=%s)", acc)
		}
		if bi.String() != "1234567890123456789012345678901234567890" {
			t.Fatalf("wrong integer %s", bi.String())
		}
		rec.checks(t, 1, 0, "true")
	})

	t.Run("unknown condition, identical big integers stay known", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.Number, valueBranch(big1)),
			rec.branch(false, cty.Number, valueBranch(big1)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.IsKnown() || !got.RawEquals(big1) {
			t.Fatalf("identical big integers should stay known, got %#v", got)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("unknown condition, differing big integers keep numeric range", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.Number, valueBranch(big1)),
			rec.branch(false, cty.Number, valueBranch(big2)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() {
			t.Fatalf("differing big integers should produce unknown result, got %#v", got)
		}
		unmarked, _ := got.Unmark()
		rng := unmarked.Range()
		minVal, minInc := rng.NumberLowerBound()
		maxVal, maxInc := rng.NumberUpperBound()
		if !minInc || !maxInc || !minVal.RawEquals(big1) || !maxVal.RawEquals(big2) {
			t.Fatalf("wrong numeric bounds: min=%#v(%v) max=%#v(%v)", minVal, minInc, maxVal, maxInc)
		}
		rec.checks(t, 1, 1, "true", "false")
	})
}

func TestCondUnknownSameAndDifferentValues(t *testing.T) {
	t.Run("identical values stay known", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, valueBranch(cty.StringVal("a"))),
			rec.branch(false, cty.String, valueBranch(cty.StringVal("a"))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.IsKnown() || !got.RawEquals(cty.StringVal("a")) {
			t.Fatalf("identical values should stay known, got %#v", got)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("identical values with different marks become unknown with both marks", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, valueBranch(cty.StringVal("a").Mark("t"))),
			rec.branch(false, cty.String, valueBranch(cty.StringVal("a").Mark("f"))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() {
			t.Fatalf("values with differing marks should not stay known, got %#v", got)
		}
		if !got.HasMark("t") || !got.HasMark("f") {
			t.Fatalf("result should carry both branch marks, got %#v", got)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("different strings keep common non-nullness and prefix", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, valueBranch(cty.StringVal("uhh, yes"))),
			rec.branch(false, cty.String, valueBranch(cty.StringVal("uhh, no"))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := cty.UnknownVal(cty.String).Refine().
			NotNull().
			StringPrefixFull("uhh, ").
			NewValue()
		if !got.RawEquals(want) {
			t.Fatalf("wrong result %#v; want %#v", got, want)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("one null and one non-null allows null", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.String, valueBranch(cty.StringVal("yes"))),
			rec.branch(false, cty.String, valueBranch(cty.NullVal(cty.String))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := cty.UnknownVal(cty.String) // not refined NotNull
		if !got.RawEquals(want) {
			t.Fatalf("wrong result %#v; want %#v", got, want)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("different numbers keep numeric range", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.Number, valueBranch(cty.NumberIntVal(1))),
			rec.branch(false, cty.Number, valueBranch(cty.NumberIntVal(3))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		unmarked, _ := got.Unmark()
		rng := unmarked.Range()
		minVal, _ := rng.NumberLowerBound()
		maxVal, _ := rng.NumberUpperBound()
		if !minVal.RawEquals(cty.NumberIntVal(1)) || !maxVal.RawEquals(cty.NumberIntVal(3)) {
			t.Fatalf("wrong numeric bounds: min=%#v max=%#v", minVal, maxVal)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("different-length lists keep length range", func(t *testing.T) {
		var rec condRecorder
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.List(cty.Number), valueBranch(cty.ListVal([]cty.Value{cty.NumberIntVal(1)}))),
			rec.branch(false, cty.List(cty.Number), valueBranch(cty.ListVal([]cty.Value{
				cty.NumberIntVal(1), cty.NumberIntVal(2),
			}))),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() {
			t.Fatalf("differing lists should produce unknown result, got %#v", got)
		}
		unmarked, _ := got.Unmark()
		rng := unmarked.Range()
		if rng.LengthLowerBound() != 1 || rng.LengthUpperBound() != 2 {
			t.Fatalf("wrong length bounds: %d..%d", rng.LengthLowerBound(), rng.LengthUpperBound())
		}
		if !rng.DefinitelyNotNull() {
			t.Fatal("both lists non-null should prove non-nullness")
		}
		rec.checks(t, 1, 1, "true", "false")
	})
}

func TestCondMarksPropagation(t *testing.T) {
	provA := testProvMark{name: "a"}
	provB := testProvMark{name: "b"}
	provUnselected := testProvMark{name: "unselected"}

	t.Run("known condition keeps selected branch deep marks only", func(t *testing.T) {
		var rec condRecorder
		selected := cty.ListVal([]cty.Value{
			cty.StringVal("a").Mark(provA),
		}).Mark("t")
		unselected := cty.ListVal([]cty.Value{
			cty.StringVal("a").Mark(provUnselected),
		}).Mark("u")
		got, err := Cond(
			cty.True.Mark("?"),
			rec.branch(true, cty.List(cty.String), valueBranch(selected)),
			rec.branch(false, cty.List(cty.String), valueBranch(unselected)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.HasMark("?") {
			t.Fatal("condition mark not propagated")
		}
		if !got.HasMark("t") {
			t.Fatal("selected branch direct mark lost")
		}
		if got.HasMark("u") {
			t.Fatal("unselected branch direct mark leaked into result")
		}
		if !got.HasMarkDeep(provA) {
			t.Fatal("selected branch deep provenance mark lost")
		}
		if got.HasMarkDeep(provUnselected) {
			t.Fatal("unselected branch deep mark leaked into result")
		}
		rec.checks(t, 1, 0, "true")
	})

	t.Run("unknown condition keeps nested provenance marks from both branches", func(t *testing.T) {
		var rec condRecorder
		trueVal := cty.ListVal([]cty.Value{
			cty.StringVal("a").Mark(provA),
		}).Mark("t")
		falseVal := cty.ListVal([]cty.Value{
			cty.StringVal("bb").Mark(provB),
		}).Mark("f")
		got, err := Cond(
			cty.UnknownVal(cty.Bool).Mark("?"),
			rec.branch(true, cty.List(cty.String), valueBranch(trueVal)),
			rec.branch(false, cty.List(cty.String), valueBranch(falseVal)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if !got.HasMark("?") {
			t.Fatal("condition mark not propagated")
		}
		if !got.HasMark("t") || !got.HasMark("f") {
			t.Fatalf("branch direct marks lost: %#v", got)
		}
		_, direct, nested := got.UnmarkDirectAndNested()
		if len(direct) == 0 {
			t.Fatal("expected direct marks on result")
		}
		if !nested.Has(provA) || !nested.Has(provB) {
			t.Fatalf("nested provenance marks not preserved: %#v", nested)
		}
		rec.checks(t, 1, 1, "true", "false")
	})

	t.Run("unknown nested marks are approximated for unknown collections", func(t *testing.T) {
		var rec condRecorder
		trueVal := cty.UnknownValWithNestedMarks(
			cty.List(cty.String), slices.Values([]any{provA}),
		)
		falseVal := cty.UnknownValWithNestedMarks(
			cty.List(cty.String), slices.Values([]any{provB}),
		).RefineNotNull()
		got, err := Cond(
			cty.UnknownVal(cty.Bool),
			rec.branch(true, cty.List(cty.String), valueBranch(trueVal)),
			rec.branch(false, cty.List(cty.String), valueBranch(falseVal)),
		)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		_, _, nested := got.UnmarkDirectAndNested()
		if !nested.Has(provA) || !nested.Has(provB) {
			t.Fatalf("nested provenance marks not preserved: %#v", nested)
		}
		rec.checks(t, 1, 1, "true", "false")
	})
}

func TestCondConvertsToCommonType(t *testing.T) {
	var rec condRecorder
	got, err := Cond(
		cty.True,
		rec.branch(true,
			cty.Object(map[string]cty.Type{"a": cty.String}),
			valueBranch(cty.ObjectVal(map[string]cty.Value{"a": cty.StringVal("b")})),
		),
		rec.branch(false,
			cty.Map(cty.String),
			valueBranch(cty.MapVal(map[string]cty.Value{"a": cty.StringVal("c")})),
		),
	)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.Type().Equals(cty.Map(cty.String)) {
		t.Fatalf("result not converted to common map type: %#v", got.Type())
	}
	if elem := got.AsValueMap()["a"]; !elem.RawEquals(cty.StringVal("b")) {
		t.Fatalf("wrong converted element: %#v", elem)
	}
	rec.checks(t, 1, 0, "true")
}
