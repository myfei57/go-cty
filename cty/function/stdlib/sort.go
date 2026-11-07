package stdlib

import (
	"fmt"
	"sort"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// sortKeySpecType is the type of each element of the sort-keys argument
// accepted by [StableSortByFunc].
//
// "path" is a non-empty list of strings selecting an object attribute or a
// map key at each step, "order" is either "asc" or "desc", and "nulls" is
// either "first" or "last". The null placement is independent of the sort
// order: descending order never reverses it.
var sortKeySpecType = cty.Object(map[string]cty.Type{
	"path":  cty.List(cty.String),
	"order": cty.String,
	"nulls": cty.String,
})

// StableSortByFunc is a function that stably sorts a list of objects by a
// sequence of sort keys.
//
// Each sort key is described by an object with the following attributes:
//
//   - path:  a non-empty list of strings. Each string is either an object
//     attribute name or a map key. The path is followed from each list
//     element to reach the value used as the sort key.
//   - order: "asc" or "desc", selecting ascending or descending order.
//   - nulls: "first" or "last", selecting whether null keys sort before or
//     after non-null keys. The null placement does not reverse when order is
//     "desc".
//
// Keys are compared in order: the next key is consulted only when two
// records compare equal on all earlier keys. Records that compare equal on
// every key keep their original positions, so the sort is stable.
//
// Sort key values must be strings, numbers, or booleans; numbers are compared
// with arbitrary precision (including positive and negative infinity) and
// strings by their NFC-normalized byte order, with false sorting before true.
// Records compared against each other by a key must hold values of the same
// type for that key.
//
// A map key that is absent from a record produces an error (it is never
// treated as null). If a key that actually participates in ordering is
// unknown, so that the relative order of otherwise-tied records cannot be
// decided, the result is an unknown value of the same list type with its
// length constrained exactly; unknown values that never participate in a
// comparison do not prevent a known result.
//
// Sets, tuples, and custom comparison callbacks are not supported.
var StableSortByFunc = function.New(&function.Spec{
	Description: `Stably sorts a list of objects by one or more keys, each given as an object with "path" (a non-empty list of object attribute or map key names), "order" ("asc" or "desc"), and "nulls" ("first" or "last") attributes.`,
	Params: []function.Parameter{
		{
			Name:             "list",
			Description:      `The list of objects to sort.`,
			Type:             cty.DynamicPseudoType,
			AllowUnknown:     true,
			AllowDynamicType: true,
			AllowMarked:      true,
		},
		{
			Name:        "sort_keys",
			Description: `The ordered keys to sort by.`,
			Type:        cty.List(sortKeySpecType),
		},
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		listTy := args[0].Type()
		switch {
		case listTy == cty.DynamicPseudoType:
			return cty.DynamicPseudoType, nil
		case listTy.IsTupleType():
			return cty.NilType, function.NewArgErrorf(0, "tuples are not supported; provide a list of objects")
		case listTy.IsSetType():
			return cty.NilType, function.NewArgErrorf(0, "sets are not supported; provide a list of objects")
		case !listTy.IsListType():
			return cty.NilType, function.NewArgErrorf(0, "a list of objects is required, got %s", listTy.FriendlyNameForConstraint())
		}

		elemTy := listTy.ElementType()
		switch {
		case elemTy == cty.DynamicPseudoType:
			// We cannot inspect the element type statically; runtime checks
			// take over instead.
		case !elemTy.IsObjectType():
			return cty.NilType, function.NewArgErrorf(0, "a list of objects is required, not a list of %s", elemTy.FriendlyNameForConstraint())
		default:
			keys, configKnown, err := parseSortKeySpecs(args[1])
			if err != nil {
				return cty.NilType, function.NewArgError(1, err)
			}
			if configKnown {
				if err := validateSortKeys(elemTy, keys); err != nil {
					return cty.NilType, function.NewArgError(1, err)
				}
			}
		}

		return listTy, nil
	},
	RefineResult: refineNonNull,
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		input, listMarks := args[0].Unmark()

		keys, configKnown, err := parseSortKeySpecs(args[1])
		if err != nil {
			return cty.NilVal, function.NewArgError(1, err)
		}
		if !configKnown {
			// The sort configuration is not yet known, so neither the order
			// nor the keys to validate can be determined.
			return cty.UnknownVal(retType).RefineNotNull(), nil
		}
		if elemTy := input.Type().ElementType(); elemTy != cty.DynamicPseudoType {
			if err := validateSortKeys(elemTy, keys); err != nil {
				return cty.NilVal, function.NewArgError(1, err)
			}
		}

		if !input.IsKnown() {
			return cty.UnknownVal(retType).RefineNotNull().WithMarks(listMarks), nil
		}

		length := input.LengthInt()
		if length <= 1 {
			// Empty and single-element lists need no value comparisons; the
			// configuration was still validated above. The input is returned
			// verbatim with its top-level marks.
			return input.WithMarks(listMarks), nil
		}

		records := input.AsValueSlice()
		planner := &stableSortPlanner{
			records: records,
			keys:    keys,
		}
		group := make([]int, length)
		for i := range group {
			group[i] = i
		}
		ordered, unknown, err := planner.orderGroup(0, group)
		if err != nil {
			return cty.NilVal, function.NewArgError(0, err)
		}

		resultMarks := cty.NewValueMarks(listMarks, planner.decisionMarks)
		if unknown {
			// Some comparison that decides the final permutation depends on
			// an unknown value, so report an unknown list of exactly the
			// input length rather than guessing a position for it.
			return cty.UnknownVal(retType).
				Refine().
				NotNull().
				CollectionLength(length).
				NewValue().
				WithMarks(resultMarks), nil
		}

		orderedVals := make([]cty.Value, length)
		for i, recIdx := range ordered {
			orderedVals[i] = records[recIdx]
		}
		return cty.ListVal(orderedVals).WithMarks(resultMarks), nil
	},
})

// stableSortKey is a parsed and validated entry of the sort-keys argument.
type stableSortKey struct {
	steps      []string
	descending bool
	nullsFirst bool
	pathText   string
}

// stableSortPlanner performs recursive, stable, lexicographic ordering while
// collecting the marks from values that actually participate in deciding the
// order.
type stableSortPlanner struct {
	records       []cty.Value
	keys          []stableSortKey
	decisionMarks cty.ValueMarks
}

type sortKeyBucket struct {
	value   cty.Value
	members []int
}

// orderGroup returns the records in group (which are already tied on all
// keys before keyIndex) in sorted order. The boolean result is true if the
// order cannot be decided because a participating key is unknown.
func (p *stableSortPlanner) orderGroup(keyIndex int, group []int) ([]int, bool, error) {
	if keyIndex == len(p.keys) || len(group) <= 1 {
		// Fully tied, or a record separated from all others by earlier keys:
		// keep the current (original-relative) order without deeper checks.
		return group, false, nil
	}
	key := p.keys[keyIndex]

	var nullMembers []int
	var buckets []*sortKeyBucket
	hasUnknown := false
	for _, recIdx := range group {
		value, marks, err := resolveSortKeyValue(p.records[recIdx], key, recIdx)
		if err != nil {
			return nil, false, err
		}
		p.decisionMarks = cty.NewValueMarks(p.decisionMarks, marks)
		switch {
		case value.IsNull():
			nullMembers = append(nullMembers, recIdx)
		case !value.IsKnown():
			hasUnknown = true
		default:
			var bucket *sortKeyBucket
			for _, candidate := range buckets {
				if sortKeysEqual(candidate.value, value) {
					bucket = candidate
					break
				}
			}
			if bucket == nil {
				bucket = &sortKeyBucket{value: value}
				buckets = append(buckets, bucket)
			}
			bucket.members = append(bucket.members, recIdx)
		}
	}
	if hasUnknown {
		// At least one record tied on all previous keys has an unknown value
		// for this key, so its position relative to the others in the group
		// cannot be decided.
		return nil, true, nil
	}

	// Every non-null value that is actually compared against another for this
	// key must have the same type.
	if len(buckets) > 1 {
		firstType := buckets[0].value.Type()
		for _, bucket := range buckets[1:] {
			if bucket.value.Type() != firstType {
				return nil, false, fmt.Errorf(
					"records %d and %d have %s values of different types (%s and %s) for sort key %s; values compared by the same sort key must have the same type",
					buckets[0].members[0], bucket.members[0],
					key.pathText,
					firstType.FriendlyNameForConstraint(), bucket.value.Type().FriendlyNameForConstraint(),
					key.pathText,
				)
			}
		}
	}

	sort.SliceStable(buckets, func(i, j int) bool {
		return compareKnownSortKeys(buckets[i].value, buckets[j].value) < 0
	})
	if key.descending {
		// Reverse only the non-null value order; the null bucket is placed
		// independently below.
		for i, j := 0, len(buckets)-1; i < j; i, j = i+1, j-1 {
			buckets[i], buckets[j] = buckets[j], buckets[i]
		}
	}

	result := make([]int, 0, len(group))
	var childErr error
	childUnknown := false
	appendMembers := func(members []int) {
		if childErr != nil || childUnknown {
			return
		}
		ordered, unknown, err := p.orderGroup(keyIndex+1, members)
		if err != nil {
			childErr = err
			return
		}
		if unknown {
			childUnknown = true
			return
		}
		result = append(result, ordered...)
	}

	if key.nullsFirst {
		appendMembers(nullMembers)
	}
	for _, bucket := range buckets {
		appendMembers(bucket.members)
	}
	if !key.nullsFirst {
		appendMembers(nullMembers)
	}

	if childErr != nil {
		return nil, false, childErr
	}
	if childUnknown {
		return nil, true, nil
	}
	return result, false, nil
}

// parseSortKeySpecs parses the sort-keys argument. The boolean result is
// false if the configuration (or any part of it) is unknown, in which case
// the sort result must be unknown.
func parseSortKeySpecs(value cty.Value) ([]stableSortKey, bool, error) {
	if !value.IsKnown() {
		return nil, false, nil
	}
	if value.IsNull() {
		return nil, false, fmt.Errorf("sort keys must not be null")
	}

	count := value.LengthInt()
	keys := make([]stableSortKey, 0, count)
	for i := 0; i < count; i++ {
		element := value.Index(cty.NumberIntVal(int64(i)))
		if element.IsNull() {
			return nil, false, fmt.Errorf("sort key at index %d must not be null", i)
		}
		if !element.IsWhollyKnown() {
			return nil, false, nil
		}

		pathValue := element.GetAttr("path")
		orderValue := element.GetAttr("order")
		nullsValue := element.GetAttr("nulls")
		if pathValue.IsNull() {
			return nil, false, fmt.Errorf("sort key at index %d has a null path", i)
		}
		if orderValue.IsNull() {
			return nil, false, fmt.Errorf("sort key at index %d has a null order; use \"asc\" or \"desc\"", i)
		}
		if nullsValue.IsNull() {
			return nil, false, fmt.Errorf("sort key at index %d has a null nulls value; use \"first\" or \"last\"", i)
		}

		steps := make([]string, 0, pathValue.LengthInt())
		for it := pathValue.ElementIterator(); it.Next(); {
			_, step := it.Element()
			steps = append(steps, step.AsString())
		}
		if len(steps) == 0 {
			return nil, false, fmt.Errorf("sort key at index %d has an empty path; at least one attribute or map key is required", i)
		}

		var descending, nullsFirst bool
		switch orderValue.AsString() {
		case "asc":
		case "desc":
			descending = true
		default:
			return nil, false, fmt.Errorf("sort key at index %d has invalid order %q; must be \"asc\" or \"desc\"", i, orderValue.AsString())
		}
		switch nullsValue.AsString() {
		case "first":
			nullsFirst = true
		case "last":
		default:
			return nil, false, fmt.Errorf("sort key at index %d has invalid nulls value %q; must be \"first\" or \"last\"", i, nullsValue.AsString())
		}

		keys = append(keys, stableSortKey{
			steps:      steps,
			descending: descending,
			nullsFirst: nullsFirst,
			pathText:   fmt.Sprintf("%q", steps),
		})
	}
	return keys, true, nil
}

// validateSortKeys statically checks that every path can be followed through
// elemTy (an object type) and ends at a string, number, or boolean. Sets and
// tuples encountered along the way are rejected.
func validateSortKeys(elemTy cty.Type, keys []stableSortKey) error {
	if len(keys) == 0 {
		return fmt.Errorf("at least one sort key is required")
	}
	for keyIndex, key := range keys {
		current := elemTy
		for stepIndex, step := range key.steps {
			if current == cty.DynamicPseudoType {
				// Nothing more can be checked statically; runtime resolution
				// enforces the same rules.
				break
			}
			switch {
			case current.IsObjectType():
				if !current.HasAttribute(step) {
					return fmt.Errorf(
						"sort key %d (%s) is not valid: the object type has no attribute %q at step %d",
						keyIndex, key.pathText, step, stepIndex,
					)
				}
				current = current.AttributeType(step)
			case current.IsMapType():
				current = current.ElementType()
			case current.IsSetType():
				return fmt.Errorf(
					"sort key %d (%s) is not valid: sets are not supported as sort key paths",
					keyIndex, key.pathText,
				)
			case current.IsTupleType() || current.IsListType():
				return fmt.Errorf(
					"sort key %d (%s) is not valid: only object attributes and map keys may be traversed; lists and tuples are not supported",
					keyIndex, key.pathText,
				)
			default:
				return fmt.Errorf(
					"sort key %d (%s) is not valid: cannot traverse into %s at step %d",
					keyIndex, key.pathText, current.FriendlyNameForConstraint(), stepIndex,
				)
			}
		}
		if current == cty.DynamicPseudoType {
			continue
		}
		switch current {
		case cty.String, cty.Number, cty.Bool:
		default:
			return fmt.Errorf(
				"sort key %d (%s) must resolve to a string, number, or boolean, but resolves to %s",
				keyIndex, key.pathText, current.FriendlyNameForConstraint(),
			)
		}
	}
	return nil
}

// resolveSortKeyValue follows key through record and returns the terminal
// value (unmarked), the marks encountered anywhere along the path, and an
// error. Traversing an attribute or element of a null object or map yields
// null, while a missing map key on a known map is an error rather than null.
func resolveSortKeyValue(record cty.Value, key stableSortKey, recordIndex int) (cty.Value, cty.ValueMarks, error) {
	var marks cty.ValueMarks
	current := record
	for _, step := range key.steps {
		var stepMarks cty.ValueMarks
		current, stepMarks = current.Unmark()
		marks = cty.NewValueMarks(marks, stepMarks)

		ty := current.Type()
		switch {
		case ty == cty.DynamicPseudoType:
			if current.IsNull() {
				// A null value of unknown concrete type means anything beneath
				// it is absent and therefore null.
				return cty.NullVal(cty.DynamicPseudoType), marks, nil
			}
			// The concrete type beneath this position is not yet known, so
			// neither is the resulting key.
			return cty.DynamicVal, marks, nil
		case ty.IsObjectType():
			if !ty.HasAttribute(step) {
				return cty.NilVal, marks, fmt.Errorf(
					"record %d has no attribute %q needed by sort path %s", recordIndex, step, key.pathText,
				)
			}
			attrTy := ty.AttributeType(step)
			switch {
			case current.IsNull():
				current = cty.NullVal(attrTy)
			case !current.IsKnown():
				current = cty.UnknownVal(attrTy)
			default:
				current = current.GetAttr(step)
			}
		case ty.IsMapType():
			elemTy := ty.ElementType()
			switch {
			case current.IsNull():
				current = cty.NullVal(elemTy)
			case !current.IsKnown():
				current = cty.UnknownVal(elemTy)
			default:
				keyValue := cty.StringVal(step)
				present := current.HasIndex(keyValue)
				if present.IsKnown() && present.False() {
					return cty.NilVal, marks, fmt.Errorf(
						"record %d is missing map key %q needed by sort path %s", recordIndex, step, key.pathText,
					)
				}
				current = current.Index(keyValue)
			}
		default:
			return cty.NilVal, marks, fmt.Errorf(
				"record %d cannot traverse %q into %s while following sort path %s; only object attributes and map keys are supported",
				recordIndex, step, ty.FriendlyNameForConstraint(), key.pathText,
			)
		}
	}

	terminal, terminalMarks := current.Unmark()
	marks = cty.NewValueMarks(marks, terminalMarks)
	if terminal.Type() == cty.DynamicPseudoType && !terminal.IsNull() {
		// A known value whose concrete key type is still dynamic cannot be
		// compared, so treat its position as undecidable.
		return cty.DynamicVal, marks, nil
	}
	if !terminal.IsNull() && terminal.IsKnown() {
		switch terminal.Type() {
		case cty.String, cty.Number, cty.Bool:
		default:
			return cty.NilVal, marks, fmt.Errorf(
				"record %d has sort path %s resolving to %s; only string, number, and boolean keys are supported",
				recordIndex, key.pathText, terminal.Type().FriendlyNameForConstraint(),
			)
		}
	}
	return terminal, marks, nil
}

// sortKeysEqual reports whether two known, non-null sort key values are
// equal. Values of different types are never equal.
func sortKeysEqual(a, b cty.Value) bool {
	if a.Type() != b.Type() {
		return false
	}
	return compareKnownSortKeys(a, b) == 0
}

// compareKnownSortKeys compares two known, non-null sort key values of the
// same type, returning -1, 0, or 1.
//
// Numbers are compared directly as arbitrary-precision big.Float values
// (which also orders positive and negative infinity correctly), strings by
// their NFC-normalized byte order, and booleans with false before true.
func compareKnownSortKeys(a, b cty.Value) int {
	switch a.Type() {
	case cty.Number:
		return a.AsBigFloat().Cmp(b.AsBigFloat())
	case cty.String:
		return strings.Compare(a.AsString(), b.AsString())
	case cty.Bool:
		var ai, bi int
		if a.True() {
			ai = 1
		}
		if b.True() {
			bi = 1
		}
		if ai < bi {
			return -1
		}
		if ai > bi {
			return 1
		}
		return 0
	default:
		// All key values are validated before sorting.
		panic(fmt.Sprintf("unsupported sort key type %#v", a.Type()))
	}
}

// StableSortBy stably sorts a list of objects by the given ordered sort
// keys. See the documentation of [StableSortByFunc] for the shape of the
// arguments and the supported semantics.
func StableSortBy(list, sortKeys cty.Value) (cty.Value, error) {
	return StableSortByFunc.Call([]cty.Value{list, sortKeys})
}
