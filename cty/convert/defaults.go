package convert

import (
	"errors"
	"sort"

	"github.com/zclconf/go-cty/cty"
)

// This file implements conversion with an explicit tree of default value
// rules. Unlike the plain conversions in the rest of the package, the rule
// tree can supply values for optional object attributes that are missing from
// the input. The rule tree is validated against the target type before any
// input value is ever processed, and the resulting compiled tree is immutable
// and safe to reuse across any number of conversions.
//
// The regular Convert and GetConversion behavior is intentionally left
// untouched: all of the bespoke logic in this file is layered alongside the
// existing conversions, and any position without a rule delegates to exactly
// the same conversion functions the rest of the package uses.

// A Rule is a single node in a default value rule tree. Each node corresponds
// to one position in the target type: the root corresponds to the target type
// itself, attributes correspond to object attributes, and so on.
//
// A Rule may provide a default Value for an optional object attribute, and it
// may also provide nested rules describing how values within the position are
// to be treated. The two are independent: an attribute that is present in the
// input still has the nested rules applied to it, while a missing attribute is
// replaced by the default Value and the nested rules are then applied to that
// default in turn.
//
// The zero value of Rule (a nil pointer, or a value with none of its fields
// set) applies no defaults at all.
type Rule struct {
	// Value is the default value to use when the corresponding optional
	// object attribute is absent from the input.
	//
	// A default may only be configured for an attribute that is declared
	// optional on the containing object type: a rule for a required or
	// nonexistent attribute fails validation. Defaults are never selected for
	// any other position (roots, list or map elements, tuple elements).
	//
	// The value must be safely convertible to the attribute's type. Values
	// may carry marks, which are retained whenever the default is selected.
	//
	// A Value that is explicitly null is accepted but has no effect beyond
	// what the ordinary conversion would already produce.
	Value cty.Value

	// UseForNull selects whether an explicitly-null value at the attribute
	// position is also replaced by Value. The default behavior, when false,
	// is to leave explicit null values as null and use Value only when the
	// attribute is absent.
	//
	// Unknown values are never considered absent and are never replaced by a
	// default, regardless of this setting.
	//
	// Setting UseForNull without also setting Value fails validation.
	UseForNull bool

	// Attributes provides rules for the attributes of an object type, keyed
	// by attribute name. It may only be set at a position whose target type
	// is an object type.
	Attributes map[string]*Rule

	// Element provides the single rule that applies to every element of a
	// list or map type. It may only be set at a position whose target type
	// is a list or a map type.
	Element *Rule

	// Elements provides one rule per element of a tuple type, by position.
	// It may only be set at a position whose target type is a tuple type,
	// and it must contain exactly one entry per tuple element (nil entries
	// are permitted and mean "no rule for this position").
	Elements []*Rule
}

// rulePosition records where in a target type a rule sits, which controls
// whether a default Value is permissible there.
type rulePosition int

const (
	posRoot rulePosition = iota
	posAttribute
	posCollectionElement
	posTupleElement
)

// compiledRule is the validated, immutable counterpart of Rule.
type compiledRule struct {
	hasDefault bool
	defaultVal cty.Value
	useForNull bool

	attrs    map[string]*compiledRule
	element  *compiledRule
	elements []*compiledRule
}

func (cr *compiledRule) isEmpty() bool {
	if cr.hasDefault || cr.useForNull {
		return false
	}
	if len(cr.attrs) != 0 || cr.element != nil || len(cr.elements) != 0 {
		return false
	}
	return true
}

// GetConversionWithDefaults returns a safe [Conversion] from values of any
// input type to the given output type that applies the given tree of default
// value rules while converting, or returns an error if the rule tree is not
// compatible with the output type.
//
// The rule tree is validated once, when this function is called, and the
// returned Conversion may then be reused any number of times with different
// input values. A failed conversion never mutates either the rule tree or the
// input value, so the same returned Conversion can be used again after an
// error.
//
// Only conversions considered "safe" are available for the input values
// themselves; default values are always required to be safely convertible,
// independently of this choice. Use [GetConversionUnsafeWithDefaults] to also
// permit unsafe conversions of input values.
func GetConversionWithDefaults(want cty.Type, rules *Rule) (Conversion, error) {
	return getConversionWithDefaults(want, rules, false)
}

// GetConversionUnsafeWithDefaults is like [GetConversionWithDefaults] except
// that the returned conversion may also make use of conversions that are
// considered "unsafe" because they can fail for some concrete input values, in
// which case the conversion returns an error describing the offending path
// when applied to such a value.
func GetConversionUnsafeWithDefaults(want cty.Type, rules *Rule) (Conversion, error) {
	return getConversionWithDefaults(want, rules, true)
}

func getConversionWithDefaults(want cty.Type, rules *Rule, unsafe bool) (Conversion, error) {
	compiled, err := compileRule(rules, want, cty.Path(nil), posRoot)
	if err != nil {
		return nil, err
	}
	return func(in cty.Value) (cty.Value, error) {
		return convertWithDefaults(in, want, compiled, unsafe, cty.Path(nil))
	}, nil
}

// ConvertWithDefaults converts the given value to the given type using the
// given tree of default value rules, permitting both safe and unsafe
// conversions of the input value.
//
// This is a convenience wrapper around [GetConversionUnsafeWithDefaults] for
// callers that apply a rule tree to a single value. The rules are validated
// against the target type before the input is inspected, so an invalid rule
// tree produces an error even for an input that would otherwise convert
// without touching any default.
func ConvertWithDefaults(in cty.Value, want cty.Type, rules *Rule) (cty.Value, error) {
	return convertWithDefaultsKind(in, want, rules, true)
}

// ConvertSafeWithDefaults is like [ConvertWithDefaults] except that only safe
// conversions of the input value are permitted, in the same sense as
// [GetConversion].
func ConvertSafeWithDefaults(in cty.Value, want cty.Type, rules *Rule) (cty.Value, error) {
	return convertWithDefaultsKind(in, want, rules, false)
}

func convertWithDefaultsKind(in cty.Value, want cty.Type, rules *Rule, unsafe bool) (cty.Value, error) {
	compiled, err := compileRule(rules, want, cty.Path(nil), posRoot)
	if err != nil {
		return cty.NilVal, err
	}
	return convertWithDefaults(in, want, compiled, unsafe, cty.Path(nil))
}

// compileRule validates the rule tree rooted at rules against the given target
// type and produces an immutable compiled tree. A nil or empty rule compiles
// to nil, which the application phase treats as "delegate to the ordinary
// conversion".
func compileRule(rules *Rule, want cty.Type, path cty.Path, position rulePosition) (*compiledRule, error) {
	if rules == nil {
		return nil, nil
	}

	cr := &compiledRule{}

	// Default values are only meaningful at optional attribute positions;
	// the object case below is responsible for checking that the attribute
	// is actually optional before compiling its rule with posAttribute.
	if rules.Value != cty.NilVal {
		if position != posAttribute {
			return nil, path.NewErrorf("default values may only be specified for optional attributes of an object type")
		}
		def := rules.Value
		// Values whose type already matches are taken as-is, mirroring the
		// identity short-circuit used by Convert: the conversion functions
		// intentionally report no conversion for identical primitive types.
		//
		// For anything else we only verify here that a safe conversion exists;
		// the conversion itself is deferred until a default is actually
		// selected. Converting eagerly would materialize optional attributes
		// omitted by the default as explicit nulls, making it impossible for
		// child rules to distinguish a missing attribute from a deliberately
		// null one and breaking the documented parent-then-child ordering.
		if !def.Type().Equals(want.WithoutOptionalAttributesDeep()) {
			if getConversion(def.Type(), want, false) == nil {
				return nil, path.NewErrorf(
					"default value of type %s cannot be safely converted to %s",
					def.Type().FriendlyNameForConstraint(),
					want.FriendlyNameForConstraint(),
				)
			}
		}
		cr.hasDefault = true
		cr.defaultVal = def
	}
	if rules.UseForNull {
		if rules.Value == cty.NilVal {
			return nil, path.NewErrorf("UseForNull is set without a default Value")
		}
		cr.useForNull = true
	}

	nestedKinds := 0
	if len(rules.Attributes) != 0 {
		nestedKinds++
	}
	if rules.Element != nil {
		nestedKinds++
	}
	if len(rules.Elements) != 0 {
		nestedKinds++
	}
	if nestedKinds > 1 {
		return nil, path.NewErrorf("default rule mixes nested rules of different kinds; only one of Attributes, Element, or Elements may be set")
	}

	switch {
	case want == cty.DynamicPseudoType:
		if nestedKinds != 0 {
			return nil, path.NewErrorf("nested default rules cannot be defined for the dynamic pseudo-type")
		}
	case want.IsPrimitiveType() || want.IsCapsuleType():
		if nestedKinds != 0 {
			return nil, path.NewErrorf("nested default rules cannot be defined for %s", want.FriendlyNameForConstraint())
		}
	case want.IsSetType():
		if nestedKinds != 0 {
			return nil, path.NewErrorf("default rules for set elements are not supported")
		}
	case want.IsObjectType():
		if rules.Element != nil || len(rules.Elements) != 0 {
			return nil, path.NewErrorf("a default rule for an object type may only use Attributes")
		}
		if len(rules.Attributes) != 0 {
			attrTypes := want.AttributeTypes()
			optionals := want.OptionalAttributes()
			names := make([]string, 0, len(rules.Attributes))
			for name := range rules.Attributes {
				names = append(names, name)
			}
			sort.Strings(names)
			cr.attrs = make(map[string]*compiledRule, len(names))
			for _, rawName := range names {
				name := cty.NormalizeString(rawName)
				attrPath := path.GetAttr(name)
				attrRule := rules.Attributes[rawName]
				if attrRule == nil {
					continue
				}
				attrTy, exists := attrTypes[name]
				if !exists {
					return nil, attrPath.NewErrorf("default rule refers to attribute %q, which does not exist", name)
				}
				if attrRule.Value != cty.NilVal {
					if _, optional := optionals[name]; !optional {
						return nil, attrPath.NewErrorf("a default value is not allowed for required attribute %q", name)
					}
				}
				compiled, err := compileRule(attrRule, attrTy, attrPath, posAttribute)
				if err != nil {
					return nil, err
				}
				if compiled != nil {
					cr.attrs[name] = compiled
				}
			}
		}
	case want.IsListType() || want.IsMapType():
		if len(rules.Attributes) != 0 || len(rules.Elements) != 0 {
			return nil, path.NewErrorf("a default rule for %s may only use Element", want.FriendlyNameForConstraint())
		}
		if rules.Element != nil {
			elementPath := path.Index(elementRuleKey(want))
			compiled, err := compileRule(rules.Element, want.ElementType(), elementPath, posCollectionElement)
			if err != nil {
				return nil, err
			}
			cr.element = compiled
		}
	case want.IsTupleType():
		if rules.Element != nil || len(rules.Attributes) != 0 {
			return nil, path.NewErrorf("a default rule for a tuple type may only use Elements")
		}
		if len(rules.Elements) != 0 {
			elemTypes := want.TupleElementTypes()
			if len(rules.Elements) != len(elemTypes) {
				return nil, path.NewErrorf(
					"tuple default rule has %d Elements but the tuple type has %d elements",
					len(rules.Elements), len(elemTypes),
				)
			}
			cr.elements = make([]*compiledRule, len(elemTypes))
			for i, elemRule := range rules.Elements {
				elemPath := path.IndexInt(i)
				compiled, err := compileRule(elemRule, elemTypes[i], elemPath, posTupleElement)
				if err != nil {
					return nil, err
				}
				cr.elements[i] = compiled
			}
		}
	default:
		// All type kinds should be covered above; leaving this here makes a
		// future new type kind fail loudly rather than silently ignore rules.
		if nestedKinds != 0 {
			return nil, path.NewErrorf("nested default rules are not supported for %s", want.FriendlyNameForConstraint())
		}
	}

	if cr.isEmpty() {
		return nil, nil
	}
	return cr, nil
}

// elementRuleKey produces the placeholder key used in the path describing a
// rule for every element of a list or map. Paths through types conventionally
// use an unknown key of the index type.
func elementRuleKey(want cty.Type) cty.Value {
	switch {
	case want.IsListType():
		return cty.UnknownVal(cty.Number)
	default: // map
		return cty.UnknownVal(cty.String)
	}
}

// convertWithDefaults is the recursive application phase. It mirrors the
// mark/unknown/null handling performed by the ordinary conversion wrapper and
// delegates to the existing conversions wherever no bespoke behavior is
// needed, keeping error paths and messages identical to the rest of the
// package.
func convertWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	if in.IsMarked() {
		// Unmark during processing and re-apply the same marks to the result,
		// so marks on input containers are retained even when the conversion
		// inserts additional defaulted attributes.
		unmarked, marks := in.Unmark()
		v, err := convertWithDefaults(unmarked, want, cr, unsafe, path)
		if err != nil {
			return cty.NilVal, err
		}
		return v.WithMarks(marks), nil
	}

	if cr == nil {
		return plainConvert(in, want, unsafe, path)
	}

	if want == cty.DynamicPseudoType {
		// A rule under the dynamic pseudo-type compiles to nil, so reaching
		// here with a non-nil rule shouldn't happen, but behave like the
		// ordinary conversion regardless.
		return plainConvert(in, want, unsafe, path)
	}

	if !in.IsKnown() {
		// Unknown values are never treated as missing and never have their
		// child defaults constructed; the ordinary conversion produces the
		// suitably-typed unknown result.
		return plainConvert(in, want, unsafe, path)
	}
	if in.IsNull() {
		if cr.useForNull && cr.hasDefault && !cr.defaultVal.IsNull() {
			// Explicit opt-in: replace the null with the default and then
			// apply the nested rules to the substituted value.
			return convertWithDefaults(cr.defaultVal, want, cr, unsafe, path)
		}
		// A null parent must never be constructed from child defaults.
		return plainConvert(in, want, unsafe, path)
	}

	switch {
	case want.IsObjectType():
		switch {
		case in.Type().IsObjectType():
			return convertObjectWithDefaults(in, want, cr, unsafe, path)
		case in.Type().IsMapType():
			if !unsafe {
				// Map to object conversion is only available in unsafe mode.
				return plainConvert(in, want, unsafe, path)
			}
			return convertMapToObjectWithDefaults(in, want, cr, unsafe, path)
		default:
			return plainConvert(in, want, unsafe, path)
		}
	case want.IsListType():
		return convertListWithDefaults(in, want, cr, unsafe, path)
	case want.IsMapType():
		return convertMapWithDefaults(in, want, cr, unsafe, path)
	case want.IsTupleType():
		if !in.Type().IsTupleType() {
			return plainConvert(in, want, unsafe, path)
		}
		return convertTupleWithDefaults(in, want, cr, unsafe, path)
	default:
		// Primitive, capsule, and set positions never carry non-empty rules
		// (validation forbids nested rules there), so this is equivalent to
		// the ordinary conversion.
		return plainConvert(in, want, unsafe, path)
	}
}

// plainConvert performs exactly the same conversion the package would perform
// without any default rules, including its error paths and messages.
func plainConvert(in cty.Value, want cty.Type, unsafe bool, path cty.Path) (cty.Value, error) {
	if in.Type().Equals(want.WithoutOptionalAttributesDeep()) {
		// Identity short-circuit, matching Convert. Note that getConversion
		// intentionally reports no conversion between identical primitive
		// types.
		return in, nil
	}
	conv := getConversion(in.Type(), want, unsafe)
	if conv == nil {
		err := errors.New(MismatchMessage(in.Type(), want))
		if len(path) != 0 {
			// Attribute the type-level mismatch to the position we were
			// asked to convert, so nested failures remain path errors just
			// like runtime conversion failures.
			return cty.NilVal, path.NewError(err)
		}
		return cty.NilVal, err
	}
	return conv(in, path)
}

// stripNullOptionalAttrs normalizes null values the same way the ordinary
// structural conversions do, ensuring results never carry optional attribute
// constraints in their embedded types.
func stripNullOptionalAttrs(v cty.Value) cty.Value {
	if v.IsNull() {
		return cty.NullVal(v.Type().WithoutOptionalAttributesDeep())
	}
	return v
}

func convertObjectWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	attrTypes := want.AttributeTypes()
	optionals := want.OptionalAttributes()
	attrVals := make(map[string]cty.Value, len(attrTypes))

	attrPath := append(path.Copy(), nil)
	for it := in.ElementIterator(); it.Next(); {
		nameVal, val := it.Element()
		name := nameVal.AsString()
		attrTy, exists := attrTypes[name]
		if !exists {
			// Attributes not present in the target type are discarded, just
			// as in the ordinary object conversion.
			continue
		}
		attrPath[len(attrPath)-1] = cty.GetAttrStep{Name: name}
		var attrRule *compiledRule
		if cr.attrs != nil {
			attrRule = cr.attrs[name]
		}
		val, err := convertWithDefaults(val, attrTy, attrRule, unsafe, attrPath)
		if err != nil {
			return cty.NilVal, err
		}
		attrVals[name] = stripNullOptionalAttrs(val)
	}

	// Fill in whatever the input omitted. Names are sorted only so that any
	// error is deterministic; the resulting object is order-independent.
	names := make([]string, 0, len(attrTypes))
	for name := range attrTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, exists := attrVals[name]; exists {
			continue
		}
		attrPath := path.GetAttr(name)
		attrTy := attrTypes[name]
		attrRule := cr.attrs[name]
		if attrRule != nil && attrRule.hasDefault {
			// Validation guarantees this attribute is optional. Substitute
			// the parent default and then recurse so that the default's own
			// missing optional attributes can be filled by child rules.
			val, err := convertWithDefaults(attrRule.defaultVal, attrTy, attrRule, unsafe, attrPath)
			if err != nil {
				return cty.NilVal, err
			}
			attrVals[name] = stripNullOptionalAttrs(val)
			continue
		}
		if _, optional := optionals[name]; optional {
			attrVals[name] = cty.NullVal(attrTy.WithoutOptionalAttributesDeep())
			continue
		}
		return cty.NilVal, attrPath.NewErrorf("attribute %q is required", name)
	}

	return cty.ObjectVal(attrVals), nil
}

func convertMapToObjectWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	attrTypes := want.AttributeTypes()
	optionals := want.OptionalAttributes()
	attrVals := make(map[string]cty.Value, len(attrTypes))

	elemPath := append(path.Copy(), nil)
	for it := in.ElementIterator(); it.Next(); {
		keyVal, val := it.Element()
		name := keyVal.AsString()
		attrTy, exists := attrTypes[name]
		if !exists {
			continue
		}
		elemPath[len(elemPath)-1] = cty.IndexStep{Key: keyVal}
		var attrRule *compiledRule
		if cr.attrs != nil {
			attrRule = cr.attrs[name]
		}
		converted, err := convertWithDefaults(val, attrTy, attrRule, unsafe, elemPath)
		if err != nil {
			if attrRule == nil {
				// Mirror the ordinary map-to-object conversion's special
				// message for an element whose type cannot be converted.
				return cty.NilVal, elemPath.NewErrorf("map element type is incompatible with attribute %q: %s", name, err)
			}
			return cty.NilVal, err
		}
		attrVals[name] = stripNullOptionalAttrs(converted)
	}

	names := make([]string, 0, len(attrTypes))
	for name := range attrTypes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, exists := attrVals[name]; exists {
			continue
		}
		attrTy := attrTypes[name]
		attrRule := cr.attrs[name]
		if attrRule != nil && attrRule.hasDefault {
			attrPath := path.GetAttr(name)
			val, err := convertWithDefaults(attrRule.defaultVal, attrTy, attrRule, unsafe, attrPath)
			if err != nil {
				return cty.NilVal, err
			}
			attrVals[name] = stripNullOptionalAttrs(val)
			continue
		}
		if _, optional := optionals[name]; optional {
			attrVals[name] = cty.NullVal(attrTy.WithoutOptionalAttributesDeep())
			continue
		}
		return cty.NilVal, path.NewErrorf("map has no element for required attribute %q", name)
	}

	return cty.ObjectVal(attrVals), nil
}

func convertListWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	inTy := in.Type()
	fromTuple := false
	switch {
	case inTy.IsListType():
	case inTy.IsSetType():
		if !in.Length().IsKnown() {
			// A set with unknown length produces an unknown list, as in the
			// ordinary set-to-list conversion.
			return cty.UnknownVal(cty.List(want.ElementType().WithoutOptionalAttributesDeep())), nil
		}
	case inTy.IsTupleType():
		fromTuple = true
	default:
		return plainConvert(in, want, unsafe, path)
	}

	ety := want.ElementType()
	elems := make([]cty.Value, 0)
	i := int64(0)
	elemPath := append(path.Copy(), nil)
	for it := in.ElementIterator(); it.Next(); {
		_, val := it.Element()
		elemPath[len(elemPath)-1] = cty.IndexStep{Key: cty.NumberIntVal(i)}
		val, err := convertWithDefaults(val, ety, cr.element, unsafe, elemPath)
		if err != nil {
			return cty.NilVal, err
		}
		elems = append(elems, stripNullOptionalAttrs(val))
		i++
	}

	if len(elems) == 0 {
		return cty.ListValEmpty(ety.WithoutOptionalAttributesDeep()), nil
	}

	if fromTuple {
		// Tuple elements may have heterogeneous types that need unifying, as
		// in the ordinary tuple-to-list conversion.
		var err error
		elems, err = conversionUnifyListElements(elems, elemPath, unsafe)
		if err != nil {
			return cty.NilVal, err
		}
	}

	if !cty.CanListVal(elems) {
		return cty.NilVal, path.NewErrorf("element types must all match for conversion to list")
	}
	return cty.ListVal(elems), nil
}

func convertMapWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	inTy := in.Type()
	if !inTy.IsMapType() && !inTy.IsObjectType() {
		return plainConvert(in, want, unsafe, path)
	}

	ety := want.ElementType()
	elems := make(map[string]cty.Value)
	elemPath := append(path.Copy(), nil)
	for it := in.ElementIterator(); it.Next(); {
		keyVal, val := it.Element()
		elemPath[len(elemPath)-1] = cty.IndexStep{Key: keyVal}
		val, err := convertWithDefaults(val, ety, cr.element, unsafe, elemPath)
		if err != nil {
			return cty.NilVal, err
		}
		elems[keyVal.AsString()] = val
	}

	if len(elems) == 0 {
		return cty.MapValEmpty(ety.WithoutOptionalAttributesDeep()), nil
	}

	if ety.IsCollectionType() || ety.IsObjectType() {
		var err error
		// The ordinary map conversion always uses safe unification here.
		elems, err = conversionUnifyCollectionElements(elems, path, false)
		if err != nil {
			return cty.NilVal, err
		}
	}

	if !cty.CanMapVal(elems) {
		return cty.NilVal, path.NewErrorf("element types must all match for conversion to map")
	}
	return cty.MapVal(elems), nil
}

func convertTupleWithDefaults(in cty.Value, want cty.Type, cr *compiledRule, unsafe bool, path cty.Path) (cty.Value, error) {
	inEtys := in.Type().TupleElementTypes()
	outEtys := want.TupleElementTypes()
	if len(inEtys) != len(outEtys) {
		// The ordinary conversion does not exist in this case either, so
		// delegate to obtain its mismatch error.
		return plainConvert(in, want, unsafe, path)
	}

	elems := make([]cty.Value, len(outEtys))
	i := 0
	elemPath := append(path.Copy(), nil)
	for it := in.ElementIterator(); it.Next(); i++ {
		_, val := it.Element()
		elemPath[len(elemPath)-1] = cty.IndexStep{Key: cty.NumberIntVal(int64(i))}
		var elemRule *compiledRule
		if cr.elements != nil {
			elemRule = cr.elements[i]
		}
		val, err := convertWithDefaults(val, outEtys[i], elemRule, unsafe, elemPath)
		if err != nil {
			return cty.NilVal, err
		}
		elems[i] = val
	}

	return cty.TupleVal(elems), nil
}
