package convert

import (
	"testing"

	"github.com/zclconf/go-cty/cty"
)

// Marks used throughout these tests. They're unexported named types, as
// recommended for application-defined marks.
type (
	defaultMark   string
	siblingMark   string
	containerMark string
)

// numInt extracts an int64 from a number value for test assertions.
func numInt(v cty.Value) int64 {
	n, _ := v.AsBigFloat().Int64()
	return n
}

// serviceElemType is list(object({name=string, port=optional(number)})),
// the central type exercised by the requirements.
func serviceElemType() cty.Type {
	return cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"name": cty.String,
		"port": cty.Number,
	}, []string{"port"})
}

func serviceListType() cty.Type {
	return cty.List(serviceElemType())
}

func serviceListRules() *Rule {
	return &Rule{
		Element: &Rule{
			Attributes: map[string]*Rule{
				"port": {Value: cty.NumberIntVal(8080)},
			},
		},
	}
}

// TestConvertWithDefaultsListElements exercises, for
// list(object({name=string, port=optional(number)})), the distinction between
// a missing port, an explicit null port, an unknown port, and a wholly unknown
// element (plus null elements and explicit zero values).
//
// A tuple is used as the input because a cty list requires homogeneous element
// types, while the situations under test deliberately produce objects with
// different shapes; tuple-to-list is an ordinary supported conversion.
func TestConvertWithDefaultsListElements(t *testing.T) {
	in := cty.TupleVal([]cty.Value{
		// Missing port: the default applies.
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("http"),
		}),
		// Explicit null port: stays null despite the default.
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("dns"),
			"port": cty.NullVal(cty.Number),
		}),
		// Unknown port: unknown is never treated as missing.
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("ntp"),
			"port": cty.UnknownVal(cty.Number),
		}),
		// Wholly unknown element of a narrower object type: must not be
		// expanded into an object containing a defaulted port.
		cty.UnknownVal(cty.Object(map[string]cty.Type{
			"name": cty.String,
		})),
		// A wholly dynamic unknown element behaves the same way.
		cty.DynamicVal,
		// A null element stays a null element.
		cty.NullVal(cty.Object(map[string]cty.Type{
			"name": cty.String,
		})),
		// An explicit zero is a present value, not a missing one.
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("privileged"),
			"port": cty.NumberIntVal(0),
		}),
	})

	got, err := ConvertWithDefaults(in, serviceListType(), serviceListRules())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	// The optional attribute constraint must be stripped from the result type.
	wantType := cty.List(cty.Object(map[string]cty.Type{
		"name": cty.String,
		"port": cty.Number,
	}))
	if !got.Type().Equals(wantType) {
		t.Fatalf("wrong result type\n got: %#v\nwant: %#v", got.Type(), wantType)
	}

	if !got.IsKnown() || got.IsNull() {
		t.Fatalf("result should be a known non-null list")
	}

	elems := got.AsValueSlice()
	if len(elems) != 7 {
		t.Fatalf("wrong element count: %d", len(elems))
	}

	checks := []struct {
		name      string
		obj       cty.Value
		portKnown bool
		portNull  bool
		portValue int64
	}{
		{"defaulted", elems[0], true, false, 8080},
		{"explicit null", elems[1], true, true, 0},
		{"unknown", elems[2], false, false, 0},
	}
	for _, check := range checks {
		port := check.obj.GetAttr("port")
		name := check.obj.GetAttr("name")
		if check.portKnown && !port.IsKnown() {
			t.Errorf("case %q: port became unknown", check.name)
		}
		if !check.portKnown && port.IsKnown() {
			t.Errorf("case %q: unknown port became known %#v", check.name, port)
		}
		if check.portNull != port.IsNull() {
			t.Errorf("case %q: wrong null-ness: %#v", check.name, port)
		}
		if check.portKnown && !check.portNull {
			if n := numInt(port); n != check.portValue {
				t.Errorf("case %q: wrong port %d", check.name, n)
			}
		}
		if !name.IsKnown() {
			t.Errorf("case %q: name should be known", check.name)
		}
	}

	if elems[0].GetAttr("name").AsString() != "http" {
		t.Errorf("defaulted element lost its name")
	}

	// The wholly unknown elements must remain wholly unknown rather than being
	// constructed from defaults.
	if elems[3].IsKnown() {
		t.Errorf("narrower unknown element was constructed: %#v", elems[3])
	}
	if elems[4].IsKnown() {
		t.Errorf("dynamic unknown element was constructed: %#v", elems[4])
	}
	// The unknown must nevertheless have the full target element type.
	if !elems[3].Type().Equals(wantType.ElementType()) {
		t.Errorf("unknown element has wrong type %#v", elems[3].Type())
	}

	if !elems[5].IsNull() {
		t.Errorf("null element should remain null: %#v", elems[5])
	}

	if port := elems[6].GetAttr("port"); port.IsNull() || numInt(port) != 0 {
		t.Errorf("explicit zero port was replaced by the default: %#v", port)
	}
}

// TestConvertWithDefaultsHomogeneousList checks the direct same-shape
// list-to-list conversion with a homogeneous element type missing the optional
// attribute.
func TestConvertWithDefaultsHomogeneousList(t *testing.T) {
	in := cty.ListVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("b")}),
	})
	got, err := ConvertWithDefaults(in, serviceListType(), serviceListRules())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	for i, elem := range got.AsValueSlice() {
		port := elem.GetAttr("port")
		if port.IsNull() || numInt(port) != 8080 {
			t.Errorf("element %d not defaulted: %#v", i, port)
		}
	}
}

// TestConvertWithDefaultsNestedParents covers the ordering between a nested
// parent default and a child rule: a missing parent is replaced by its own
// default first, and only then are child rules applied; an existing parent
// always gets the child rules directly. Null and unknown parents are never
// constructed from child defaults.
func TestConvertWithDefaultsNestedParents(t *testing.T) {
	svcType := serviceElemType()
	topType := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"svc": svcType,
	}, []string{"svc"})

	rules := &Rule{Attributes: map[string]*Rule{
		"svc": {
			Value: cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("default-svc"),
			}),
			Attributes: map[string]*Rule{
				"port": {Value: cty.NumberIntVal(8080)},
			},
		},
	}}

	tests := []struct {
		name string
		in   cty.Value
		want cty.Value
	}{
		{
			"missing parent uses parent default then child default",
			cty.EmptyObjectVal,
			cty.ObjectVal(map[string]cty.Value{
				"svc": cty.ObjectVal(map[string]cty.Value{
					"name": cty.StringVal("default-svc"),
					"port": cty.NumberIntVal(8080),
				}),
			}),
		},
		{
			"existing parent has child rules applied",
			cty.ObjectVal(map[string]cty.Value{
				"svc": cty.ObjectVal(map[string]cty.Value{
					"name": cty.StringVal("custom"),
				}),
			}),
			cty.ObjectVal(map[string]cty.Value{
				"svc": cty.ObjectVal(map[string]cty.Value{
					"name": cty.StringVal("custom"),
					"port": cty.NumberIntVal(8080),
				}),
			}),
		},
		{
			"values present in the parent default are not overwritten",
			cty.EmptyObjectVal,
			cty.ObjectVal(map[string]cty.Value{
				"svc": cty.ObjectVal(map[string]cty.Value{
					"name": cty.StringVal("default-svc"),
					"port": cty.NumberIntVal(8080),
				}),
			}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ConvertWithDefaults(test.in, topType, rules)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if !got.RawEquals(test.want) {
				t.Errorf("wrong result\ngot:  %#v\nwant: %#v", got, test.want)
			}
		})
	}

	// When the parent default already supplies a port, that value wins over
	// the child default because child rules only fill what's missing.
	rulesWithPort := &Rule{Attributes: map[string]*Rule{
		"svc": {
			Value: cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("default-svc"),
				"port": cty.NumberIntVal(9000),
			}),
			Attributes: map[string]*Rule{
				"port": {Value: cty.NumberIntVal(8080)},
			},
		},
	}}
	got, err := ConvertWithDefaults(cty.EmptyObjectVal, topType, rulesWithPort)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if port := got.GetAttr("svc").GetAttr("port"); numInt(port) != 9000 {
		t.Errorf("parent default's port was overwritten: %#v", port)
	}

	// Null and wholly unknown parents are never constructed from child
	// defaults.
	for name, in := range map[string]cty.Value{
		"null parent":    cty.ObjectVal(map[string]cty.Value{"svc": cty.NullVal(svcType)}),
		"unknown parent": cty.ObjectVal(map[string]cty.Value{"svc": cty.UnknownVal(svcType)}),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ConvertWithDefaults(in, topType, rules)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			svc := got.GetAttr("svc")
			if name == "null parent" && !svc.IsNull() {
				t.Errorf("null parent was constructed from child defaults: %#v", svc)
			}
			if name == "unknown parent" && svc.IsKnown() {
				t.Errorf("unknown parent was constructed from child defaults: %#v", svc)
			}
		})
	}

	// A missing optional parent with only child rules (no parent default of
	// its own) remains null: child defaults alone cannot conjure the object.
	childOnlyRules := &Rule{Attributes: map[string]*Rule{
		"svc": {
			Attributes: map[string]*Rule{
				"port": {Value: cty.NumberIntVal(8080)},
			},
		},
	}}
	got, err = ConvertWithDefaults(cty.EmptyObjectVal, topType, childOnlyRules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.GetAttr("svc").IsNull() {
		t.Errorf("missing parent without a default was constructed: %#v", got.GetAttr("svc"))
	}

	// A present parent that is itself invalid (missing required name) still
	// reports the ordinary conversion path error.
	_, err = ConvertWithDefaults(cty.ObjectVal(map[string]cty.Value{
		"svc": cty.EmptyObjectVal,
	}), topType, rules)
	if err == nil {
		t.Fatalf("expected error for parent missing required attribute")
	}
	if _, ok := err.(cty.PathError); !ok {
		t.Fatalf("expected a path error, got %T: %s", err, err)
	}
	if got, want := errorStrForTesting(err), `.svc.name: attribute "name" is required`; got != want {
		t.Errorf("wrong error\n got: %s\nwant: %s", got, want)
	}
}

// TestConvertWithDefaultsUseForNull checks the explicit opt-in where null is
// also replaced, while unknown still is not.
func TestConvertWithDefaultsUseForNull(t *testing.T) {
	elemType := serviceElemType()
	rules := &Rule{Attributes: map[string]*Rule{
		"port": {Value: cty.NumberIntVal(8080), UseForNull: true},
	}}

	tests := []struct {
		name        string
		in          cty.Value
		wantNull    bool
		wantUnknown bool
		wantPort    int64
	}{
		{
			name:     "missing uses default",
			in:       cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
			wantPort: 8080,
		},
		{
			name: "explicit null also uses default when opted in",
			in: cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("b"),
				"port": cty.NullVal(cty.Number),
			}),
			wantPort: 8080,
		},
		{
			name: "unknown never uses default",
			in: cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("c"),
				"port": cty.UnknownVal(cty.Number),
			}),
			wantUnknown: true,
		},
		{
			name: "explicit zero is preserved",
			in: cty.ObjectVal(map[string]cty.Value{
				"name": cty.StringVal("d"),
				"port": cty.NumberIntVal(0),
			}),
			wantPort: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ConvertWithDefaults(test.in, elemType, rules)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			port := got.GetAttr("port")
			if test.wantUnknown {
				if port.IsKnown() {
					t.Errorf("unknown port became known: %#v", port)
				}
				return
			}
			if port.IsNull() != test.wantNull {
				t.Errorf("wrong null-ness: %#v", port)
			}
			if !test.wantNull && numInt(port) != test.wantPort {
				t.Errorf("wrong port: %#v", port)
			}
		})
	}

	// Without the opt-in, explicit null remains null for the same shape.
	plainRules := &Rule{Attributes: map[string]*Rule{
		"port": {Value: cty.NumberIntVal(8080)},
	}}
	got, err := ConvertWithDefaults(cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("b"),
		"port": cty.NullVal(cty.Number),
	}), elemType, plainRules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.GetAttr("port").IsNull() {
		t.Errorf("explicit null should be preserved without UseForNull: %#v", got.GetAttr("port"))
	}
}

// TestConvertWithDefaultsNullObjectUseForNull verifies that UseForNull on an
// object-typed attribute substitutes the default object and then applies its
// child rules.
func TestConvertWithDefaultsNullObjectUseForNull(t *testing.T) {
	svcType := serviceElemType()
	topType := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"svc": svcType,
	}, []string{"svc"})
	rules := &Rule{Attributes: map[string]*Rule{
		"svc": {
			Value:      cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("from-default")}),
			UseForNull: true,
			Attributes: map[string]*Rule{
				"port": {Value: cty.NumberIntVal(8080)},
			},
		},
	}}
	got, err := ConvertWithDefaults(cty.ObjectVal(map[string]cty.Value{
		"svc": cty.NullVal(svcType),
	}), topType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	want := cty.ObjectVal(map[string]cty.Value{
		"svc": cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("from-default"),
			"port": cty.NumberIntVal(8080),
		}),
	})
	if !got.RawEquals(want) {
		t.Errorf("wrong result\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestConvertWithDefaultsEmptyContainersAndZeroValues ensures existing empty
// containers and zero values are handled distinctly from missing attributes.
func TestConvertWithDefaultsEmptyContainersAndZeroValues(t *testing.T) {
	objType := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"items":    cty.List(cty.String),
		"labels":   cty.Map(cty.String),
		"count":    cty.Number,
		"enabled":  cty.Bool,
		"hostname": cty.String,
	}, []string{"items", "labels", "count", "enabled", "hostname"})
	rules := &Rule{Attributes: map[string]*Rule{
		"items":    {Value: cty.ListVal([]cty.Value{cty.StringVal("default-item")})},
		"labels":   {Value: cty.MapVal(map[string]cty.Value{"default": cty.StringVal("label")})},
		"count":    {Value: cty.NumberIntVal(3)},
		"enabled":  {Value: cty.True},
		"hostname": {Value: cty.StringVal("default-host")},
	}}

	// Existing empty containers and explicit zero values must survive, and
	// must not be confused with missing attributes.
	in := cty.ObjectVal(map[string]cty.Value{
		"items":    cty.ListValEmpty(cty.String),
		"labels":   cty.MapValEmpty(cty.String),
		"count":    cty.NumberIntVal(0),
		"enabled":  cty.False,
		"hostname": cty.StringVal(""),
	})
	got, err := ConvertWithDefaults(in, objType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.GetAttr("items").IsKnown() || got.GetAttr("items").LengthInt() != 0 {
		t.Errorf("empty list not preserved: %#v", got.GetAttr("items"))
	}
	if !got.GetAttr("labels").IsKnown() || got.GetAttr("labels").LengthInt() != 0 {
		t.Errorf("empty map not preserved: %#v", got.GetAttr("labels"))
	}
	if n := numInt(got.GetAttr("count")); n != 0 {
		t.Errorf("zero number replaced with default: %d", n)
	}
	if got.GetAttr("enabled").True() {
		t.Errorf("false replaced with default")
	}
	if hostname := got.GetAttr("hostname").AsString(); hostname != "" {
		t.Errorf("empty string replaced with default: %q", hostname)
	}

	// Missing attributes are defaulted.
	got, err = ConvertWithDefaults(cty.EmptyObjectVal, objType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.GetAttr("items").LengthInt() != 1 {
		t.Errorf("missing list not defaulted: %#v", got.GetAttr("items"))
	}
	if got.GetAttr("labels").LengthInt() != 1 {
		t.Errorf("missing map not defaulted: %#v", got.GetAttr("labels"))
	}

	// Empty input collections keep a concrete, optional-stripped type even
	// when nested under list elements.
	listType := cty.List(serviceElemType())
	emptyIn := cty.ListValEmpty(cty.Object(map[string]cty.Type{"name": cty.String}))
	got, err = ConvertWithDefaults(emptyIn, listType, serviceListRules())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.Type().Equals(cty.List(cty.Object(map[string]cty.Type{
		"name": cty.String,
		"port": cty.Number,
	}))) {
		t.Errorf("empty list result kept optional attributes in its type: %#v", got.Type())
	}
}

// TestConvertWithDefaultsMarks ensures marks go to the right places: defaults
// keep their own marks, selected attributes keep theirs, sibling marks never
// leak onto defaults, and container marks are retained.
func TestConvertWithDefaultsMarks(t *testing.T) {
	const (
		dm defaultMark   = "default"
		sm siblingMark   = "sibling"
		cm containerMark = "container"
	)
	elemType := serviceElemType()
	rules := &Rule{Attributes: map[string]*Rule{
		"port": {Value: cty.NumberIntVal(8080).Mark(dm)},
	}}

	in := cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("a").Mark(sm),
	})
	got, err := ConvertWithDefaults(in, elemType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	name := got.GetAttr("name")
	port := got.GetAttr("port")
	if !name.HasMark(sm) {
		t.Errorf("selected attribute lost its mark: %#v", name.Marks())
	}
	if !port.HasMark(dm) {
		t.Errorf("default lost its own mark: %#v", port.Marks())
	}
	if port.HasMark(sm) {
		t.Errorf("default inherited an unrelated sibling mark: %#v", port.Marks())
	}
	if name.HasMark(dm) {
		t.Errorf("sibling attribute inherited the default's mark")
	}
	if got.IsMarked() {
		t.Errorf("object should not itself be marked")
	}

	// A mark on the input container must survive the insertion of defaults.
	inMarked := cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("a"),
	}).Mark(cm)
	got, err = ConvertWithDefaults(inMarked, elemType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.IsMarked() || !got.HasMark(cm) {
		t.Errorf("container mark disappeared: %#v", got.Marks())
	}
	// The default nested inside carries its own mark too; unmark deeply
	// before inspecting its value.
	unmarked, _ := got.UnmarkDeep()
	if port := unmarked.GetAttr("port"); port.IsNull() || numInt(port) != 8080 {
		t.Errorf("default not applied within marked container: %#v", port)
	}

	// The same applies to a marked list container with defaulted elements.
	listIn := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
	}).Mark(cm)
	got, err = ConvertWithDefaults(listIn, serviceListType(), serviceListRules())
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.HasMark(cm) {
		t.Errorf("list container mark disappeared")
	}
}

// TestConvertWithDefaultsOptionalStripping checks that optional constraints
// never survive in result types, including null defaults and element types.
func TestConvertWithDefaultsOptionalStripping(t *testing.T) {
	inner := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"name": cty.String,
		"port": cty.Number,
	}, []string{"port"})
	topType := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"a": cty.List(inner),
		"b": cty.Map(inner),
		"c": inner,
		"d": cty.String,
	}, []string{"a", "b", "c", "d"})

	got, err := ConvertWithDefaults(cty.EmptyObjectVal, topType, nil)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	stripped := topType.WithoutOptionalAttributesDeep()
	if !got.Type().Equals(stripped) {
		t.Errorf("result type still carries optional attributes\ngot:  %#v\nwant: %#v", got.Type(), stripped)
	}
	if !got.GetAttr("c").IsNull() {
		t.Errorf("expected null for missing optional object")
	}
	if !got.GetAttr("c").Type().Equals(inner.WithoutOptionalAttributesDeep()) {
		t.Errorf("null optional object kept optional attributes in its type: %#v", got.GetAttr("c").Type())
	}
}

// TestConvertWithDefaultsTuple verifies positional tuple element rules.
func TestConvertWithDefaultsTuple(t *testing.T) {
	elemType := serviceElemType()
	wantType := cty.Tuple([]cty.Type{elemType, cty.String})
	rules := &Rule{Elements: []*Rule{
		{Attributes: map[string]*Rule{
			"port": {Value: cty.NumberIntVal(8080)},
		}},
		nil, // no rule for the second element
	}}

	in := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
		cty.StringVal("second"),
	})
	got, err := ConvertWithDefaults(in, wantType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	want := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("a"),
			"port": cty.NumberIntVal(8080),
		}),
		cty.StringVal("second"),
	})
	if !got.RawEquals(want) {
		t.Errorf("wrong result\ngot:  %#v\nwant: %#v", got, want)
	}
}

// TestConvertWithDefaultsMapElements checks the shared element rule for maps,
// including when the input is an object whose attributes become map elements.
func TestConvertWithDefaultsMapElements(t *testing.T) {
	mapType := cty.Map(serviceElemType())
	rules := &Rule{Element: &Rule{Attributes: map[string]*Rule{
		"port": {Value: cty.NumberIntVal(8080)},
	}}}
	// A map value requires homogeneous element types, so heterogeneous
	// objects are supplied as an object value and converted to the target map,
	// which is an ordinary safe conversion.
	in := cty.ObjectVal(map[string]cty.Value{
		"a": cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
		"b": cty.ObjectVal(map[string]cty.Value{
			"name": cty.StringVal("b"),
			"port": cty.NullVal(cty.Number),
		}),
	})
	got, err := ConvertWithDefaults(in, mapType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	a := got.Index(cty.StringVal("a"))
	b := got.Index(cty.StringVal("b"))
	if port := a.GetAttr("port"); numInt(port) != 8080 {
		t.Errorf("map element a not defaulted: %#v", port)
	}
	if !b.GetAttr("port").IsNull() {
		t.Errorf("explicit null in map element b was replaced: %#v", b.GetAttr("port"))
	}

	// An empty input map stays an empty map with the concrete element type.
	emptyIn := cty.MapValEmpty(cty.Object(map[string]cty.Type{"name": cty.String}))
	got, err = ConvertWithDefaults(emptyIn, mapType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.LengthInt() != 0 || !got.Type().Equals(cty.Map(cty.Object(map[string]cty.Type{
		"name": cty.String,
		"port": cty.Number,
	}))) {
		t.Errorf("empty map handled incorrectly: %#v", got)
	}
}

// TestConvertWithDefaultsRuleValidation ensures every invalid rule is rejected
// by rule path before any input value is considered.
func TestConvertWithDefaultsRuleValidation(t *testing.T) {
	requiredObject := cty.Object(map[string]cty.Type{
		"a": cty.String,
	})
	optionalObject := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"a": cty.String,
	}, []string{"a"})

	tests := []struct {
		name     string
		want     cty.Type
		rules    *Rule
		wantPath string
	}{
		{
			name: "default for required attribute",
			want: requiredObject,
			rules: &Rule{Attributes: map[string]*Rule{
				"a": {Value: cty.StringVal("x")},
			}},
			wantPath: ".a",
		},
		{
			name: "default for nonexistent attribute",
			want: requiredObject,
			rules: &Rule{Attributes: map[string]*Rule{
				"b": {Value: cty.StringVal("x")},
			}},
			wantPath: ".b",
		},
		{
			name: "sub-rules on a primitive attribute",
			want: cty.Object(map[string]cty.Type{
				"a": cty.String,
			}),
			rules: &Rule{Attributes: map[string]*Rule{
				"a": {Attributes: map[string]*Rule{
					"x": {Value: cty.StringVal("y")},
				}},
			}},
			wantPath: ".a",
		},
		{
			name: "default not safely convertible",
			want: cty.ObjectWithOptionalAttrs(map[string]cty.Type{
				"a": cty.Number,
			}, []string{"a"}),
			rules: &Rule{Attributes: map[string]*Rule{
				// string to number is unsafe, so this default is rejected.
				"a": {Value: cty.StringVal("not-a-number")},
			}},
			wantPath: ".a",
		},
		{
			name: "conflicting nested rule kinds",
			want: cty.Object(map[string]cty.Type{
				"a": cty.String,
			}),
			rules: &Rule{
				Attributes: map[string]*Rule{
					"a": {},
				},
				Element: &Rule{},
			},
			wantPath: "",
		},
		{
			name: "tuple rule arity mismatch",
			want: cty.Tuple([]cty.Type{cty.String, cty.String}),
			rules: &Rule{Elements: []*Rule{
				nil,
			}},
			wantPath: "",
		},
		{
			name:     "rules for set elements",
			want:     cty.Set(serviceElemType()),
			rules:    &Rule{Element: &Rule{Attributes: map[string]*Rule{"port": {Value: cty.NumberIntVal(1)}}}},
			wantPath: "",
		},
		{
			name:     "default value at root",
			want:     optionalObject,
			rules:    &Rule{Value: cty.EmptyObjectVal},
			wantPath: "",
		},
		{
			name:     "default value at list element",
			want:     cty.List(optionalObject),
			rules:    &Rule{Element: &Rule{Value: cty.EmptyObjectVal}},
			wantPath: "[cty.UnknownVal(cty.Number)]",
		},
		{
			name: "UseForNull without value",
			want: optionalObject,
			rules: &Rule{Attributes: map[string]*Rule{
				"a": {UseForNull: true},
			}},
			wantPath: ".a",
		},
		{
			name: "children for primitive list element",
			want: cty.List(cty.String),
			rules: &Rule{Element: &Rule{Attributes: map[string]*Rule{
				"x": {Value: cty.StringVal("y")},
			}}},
			wantPath: "[cty.UnknownVal(cty.Number)]",
		},
		{
			name: "nested default for required attribute",
			want: cty.Object(map[string]cty.Type{
				"svc": requiredObject,
			}),
			rules: &Rule{Attributes: map[string]*Rule{
				"svc": {Attributes: map[string]*Rule{
					"a": {Value: cty.StringVal("x")},
				}},
			}},
			wantPath: ".svc.a",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, unsafe := range []bool{false, true} {
				var err error
				if unsafe {
					_, err = GetConversionUnsafeWithDefaults(test.want, test.rules)
				} else {
					_, err = GetConversionWithDefaults(test.want, test.rules)
				}
				if err == nil {
					t.Fatalf("unsafe=%v: expected validation error", unsafe)
				}
				pe, ok := err.(cty.PathError)
				if !ok {
					t.Fatalf("unsafe=%v: expected cty.PathError, got %T: %s", unsafe, err, err)
				}
				if got := pathStrForTesting(pe.Path); got != test.wantPath {
					t.Errorf("unsafe=%v: wrong error path\ngot:  %q\nwant: %q\nerror: %s", unsafe, got, test.wantPath, err)
				}
			}
		})
	}
}

// TestConvertWithDefaultsSafeUnsafe verifies the caller can choose safe or
// unsafe conversion for present values while runtime conversion errors retain
// their original paths and messages.
func TestConvertWithDefaultsSafeUnsafe(t *testing.T) {
	ty := cty.Object(map[string]cty.Type{
		"n": cty.Number,
	})
	in := cty.ObjectVal(map[string]cty.Value{
		"n": cty.StringVal("5"),
	})

	got, err := ConvertWithDefaults(in, ty, nil)
	if err != nil {
		t.Fatalf("unsafe conversion of a numeric string should succeed: %s", err)
	}
	if n := numInt(got.GetAttr("n")); n != 5 {
		t.Errorf("wrong converted value: %d", n)
	}

	_, err = ConvertSafeWithDefaults(in, ty, nil)
	if err == nil {
		t.Fatalf("safe conversion should reject string to number")
	}
	// The type-level impossibility surfaces through exactly the same message
	// the ordinary conversion produces for this shape.
	if got, want := errorStrForTesting(err), `attribute "n": number required`; got != want {
		t.Errorf("wrong safe error\n got: %s\nwant: %s", got, want)
	}

	// Runtime failure under unsafe conversion keeps the original primitive
	// error message and attribute path.
	bad := cty.ObjectVal(map[string]cty.Value{
		"n": cty.StringVal("hello"),
	})
	_, err = ConvertWithDefaults(bad, ty, nil)
	if err == nil {
		t.Fatalf("expected runtime conversion error")
	}
	if got, want := errorStrForTesting(err), ".n: a number is required"; got != want {
		t.Errorf("wrong runtime error\n got: %s\nwant: %s", got, want)
	}
}

// TestConvertWithDefaultsCompiledReuse checks that one compiled conversion can
// be reused after a failed call, that the rule tree remains usable, and that
// input values are never mutated.
func TestConvertWithDefaultsCompiledReuse(t *testing.T) {
	conv, err := GetConversionUnsafeWithDefaults(serviceListType(), serviceListRules())
	if err != nil {
		t.Fatalf("unexpected validation error: %s", err)
	}

	// First call fails at runtime (name is a list, cannot become a string).
	badTuple := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{
			"name": cty.ListVal([]cty.Value{cty.NumberIntVal(42)}),
		}),
	})
	badSnapshot := badTuple
	if _, err := conv(badTuple); err == nil {
		t.Fatalf("expected conversion failure")
	}
	if !badTuple.RawEquals(badSnapshot) {
		t.Errorf("input value changed after failed conversion")
	}

	// The same compiled conversion still works for a good input, repeatedly.
	good := cty.TupleVal([]cty.Value{
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("a")}),
		cty.ObjectVal(map[string]cty.Value{"name": cty.StringVal("b")}),
	})
	goodSnapshot := good
	for i := 0; i < 3; i++ {
		got, err := conv(good)
		if err != nil {
			t.Fatalf("reuse %d failed: %s", i, err)
		}
		if got.LengthInt() != 2 {
			t.Fatalf("reuse %d: wrong length", i)
		}
		for _, elem := range got.AsValueSlice() {
			if port := elem.GetAttr("port"); numInt(port) != 8080 {
				t.Fatalf("reuse %d: default not applied: %#v", i, port)
			}
		}
	}
	if !good.RawEquals(goodSnapshot) {
		t.Errorf("input value changed after conversion")
	}

	// The source rule tree must also be unchanged (the same *Rule can be
	// compiled again independently).
	if _, err := GetConversionUnsafeWithDefaults(serviceListType(), serviceListRules()); err != nil {
		t.Errorf("rule tree could not be recompiled: %s", err)
	}
}

// TestConvertWithDefaultsInputMapNotModified verifies that the supplied input
// object value is reconstructed rather than mutated in place, and that nulls
// are not uniformly replaced.
func TestConvertWithDefaultsInputMapNotModified(t *testing.T) {
	elemType := serviceElemType()
	rules := &Rule{Attributes: map[string]*Rule{
		"port": {Value: cty.NumberIntVal(8080)},
	}}
	in := cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("a"),
	})
	got, err := ConvertWithDefaults(in, elemType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	// The input still only has the single attribute it started with.
	if in.Type().HasAttribute("port") {
		t.Errorf("input type gained an attribute: %#v", in.Type())
	}
	if len(in.AsValueMap()) != 1 {
		t.Errorf("input map appears modified: %#v", in.AsValueMap())
	}
	if !got.GetAttr("name").RawEquals(cty.StringVal("a")) {
		t.Errorf("name changed")
	}

	// Explicit nulls are not uniformly replaced: the null stays null while a
	// missing attribute is defaulted, within the same object shape.
	mixed, err := ConvertWithDefaults(cty.ObjectVal(map[string]cty.Value{
		"name": cty.StringVal("b"),
		"port": cty.NullVal(cty.Number),
	}), elemType, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !mixed.GetAttr("port").IsNull() {
		t.Errorf("explicit null was uniformly replaced: %#v", mixed.GetAttr("port"))
	}
}

// TestConvertWithDefaultsNullTopLevel ensures a null or unknown top-level value
// is never built up from root attribute defaults.
func TestConvertWithDefaultsNullTopLevel(t *testing.T) {
	ty := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"a": cty.String,
	}, []string{"a"})
	rules := &Rule{Attributes: map[string]*Rule{
		"a": {Value: cty.StringVal("default")},
	}}

	nullIn := cty.NullVal(cty.DynamicPseudoType)
	got, err := ConvertWithDefaults(nullIn, ty, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.IsNull() {
		t.Errorf("null input was expanded with defaults: %#v", got)
	}

	unknownIn := cty.UnknownVal(cty.DynamicPseudoType)
	got, err = ConvertWithDefaults(unknownIn, ty, rules)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if got.IsKnown() {
		t.Errorf("unknown input was expanded with defaults: %#v", got)
	}
}
