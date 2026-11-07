package json

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

// equatableCapsuleNative is transported by capsule types that provide
// RawEquals, so round-tripped values can be compared for value equality.
type equatableCapsuleNative struct {
	Text string `json:"text"`
}

// plainCapsuleNative is transported by a capsule with default
// pointer-identity equality, where round-tripped native pointers are not
// expected to compare equal even though their content survives.
type plainCapsuleNative struct {
	N int `json:"n"`
}

// distinctCapsules constructs two capsule types with the same friendly
// name and the same reflected native Go type but with distinct cty
// identities, both providing RawEquals.
func distinctCapsules() (cty.Type, cty.Type) {
	eq := func(a, b any) bool {
		return a.(*equatableCapsuleNative).Text == b.(*equatableCapsuleNative).Text
	}
	a := cty.CapsuleWithOps("capsule", reflect.TypeOf(equatableCapsuleNative{}), &cty.CapsuleOps{
		RawEquals: eq,
	})
	b := cty.CapsuleWithOps("capsule", reflect.TypeOf(equatableCapsuleNative{}), &cty.CapsuleOps{
		RawEquals: eq,
	})
	return a, b
}

func equatableEncode(native any) (json.RawMessage, error) {
	buf, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

func equatableDecode(raw json.RawMessage) (any, error) {
	var v equatableCapsuleNative
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func plainEncode(native any) (json.RawMessage, error) {
	buf, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(buf), nil
}

func plainDecode(raw json.RawMessage) (any, error) {
	var v plainCapsuleNative
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	return &v, nil
}

func mustRegisterCapsule(t *testing.T, reg *CapsuleTypeRegistry, ty cty.Type, id string, version uint64, enc CapsuleValueEncoder, dec CapsuleValueDecoder) {
	t.Helper()
	if err := reg.RegisterCapsuleType(ty, id, version, enc, dec); err != nil {
		t.Fatalf("failed to register %s as %q version %d: %s", ty.FriendlyName(), id, version, err)
	}
}

func requirePathError(t *testing.T, err error, wantSubstring string) cty.PathError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error containing %q, got success", wantSubstring)
	}
	var pe cty.PathError
	if !errors.As(err, &pe) {
		t.Fatalf("expected cty.PathError, got %T: %s", err, err)
	}
	if wantSubstring != "" && !strings.Contains(pe.Error(), wantSubstring) {
		t.Fatalf("error %q does not contain %q", pe.Error(), wantSubstring)
	}
	return pe
}

func TestCapsuleRegistryValueRoundTrip(t *testing.T) {
	capA, _ := distinctCapsules()
	capPlain := cty.Capsule("plain", reflect.TypeOf(plainCapsuleNative{}))

	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/capsule", 1, equatableEncode, equatableDecode)
	mustRegisterCapsule(t, reg, capPlain, "example.com/plain", 1, plainEncode, plainDecode)

	t.Run("equatable capsule uses the identity envelope", func(t *testing.T) {
		orig := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "hello"})
		buf, err := reg.Marshal(orig, capA)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		want := `{"id":"example.com/capsule","version":1,"value":{"text":"hello"}}`
		if string(buf) != want {
			t.Fatalf("wrong envelope\ngot:  %s\nwant: %s", buf, want)
		}

		got, err := reg.Unmarshal(buf, capA)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.Type().Equals(capA) {
			t.Fatalf("wrong type after round trip: %#v", got.Type())
		}
		if !got.RawEquals(orig) {
			t.Fatalf("round-tripped value is not equal: got %#v want %#v", got, orig)
		}
	})

	t.Run("default pointer capsule round trips by content, not pointer identity", func(t *testing.T) {
		origNative := &plainCapsuleNative{N: 42}
		orig := cty.CapsuleVal(capPlain, origNative)
		buf, err := reg.Marshal(orig, capPlain)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		got, err := reg.Unmarshal(buf, capPlain)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.Type().Equals(capPlain) {
			t.Fatalf("wrong type after round trip: %#v", got.Type())
		}

		// RawEquals on a capsule without custom equality compares pointer
		// identity, and a fresh native value is intentionally allocated.
		if got.RawEquals(orig) {
			t.Fatalf("default pointer-identity capsule unexpectedly compared equal after round trip")
		}
		gotNative := got.EncapsulatedValue().(*plainCapsuleNative)
		if !reflect.DeepEqual(gotNative, origNative) {
			t.Fatalf("native content not preserved: got %#v want %#v", gotNative, origNative)
		}
		if gotNative == origNative {
			t.Fatalf("decoder unexpectedly reused the original native pointer")
		}
	})
}

func TestCapsuleIdentityIsNotInferredFromNameOrNativeType(t *testing.T) {
	capA, capB := distinctCapsules()

	// Sanity check the premise: the two types are indistinguishable by
	// friendly name or reflected native type.
	if capA.FriendlyName() != capB.FriendlyName() {
		t.Fatalf("test types should share a friendly name")
	}
	if capA.EncapsulatedType() != capB.EncapsulatedType() {
		t.Fatalf("test types should share the same reflected native type")
	}
	if capA.Equals(capB) {
		t.Fatalf("the two capsule types should have distinct cty identities")
	}

	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/a", 1, equatableEncode, equatableDecode)

	// Same wire identity cannot be re-bound to the other cty type.
	err := reg.RegisterCapsuleType(capB, "example.com/a", 1, equatableEncode, equatableDecode)
	if err == nil {
		t.Fatalf("expected conflict binding the same id/version to another cty type")
	}

	// The other type can have its own distinct wire identity.
	mustRegisterCapsule(t, reg, capB, "example.com/b", 1, equatableEncode, equatableDecode)

	valA := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "x"})
	bufA, err := reg.Marshal(valA, capA)
	if err != nil {
		t.Fatalf("unexpected Marshal error: %s", err)
	}

	// A descriptor for A must not decode into B.
	if _, err := reg.Unmarshal(bufA, capB); err == nil {
		t.Fatalf("expected target type mismatch error, got success")
	} else {
		requirePathError(t, err, "does not match the target type")
	}

	// Decoding against A returns A's identity, not B's lookalike.
	got, err := reg.Unmarshal(bufA, capA)
	if err != nil {
		t.Fatalf("unexpected Unmarshal error: %s", err)
	}
	if !got.Type().Equals(capA) || got.Type().Equals(capB) {
		t.Fatalf("expected the registry-held type A, got %#v", got.Type())
	}

	// Type descriptions resolve to the exact registered type identity.
	typeBuf, err := reg.MarshalType(capB)
	if err != nil {
		t.Fatalf("unexpected MarshalType error: %s", err)
	}
	gotType, err := reg.UnmarshalType(typeBuf)
	if err != nil {
		t.Fatalf("unexpected UnmarshalType error: %s", err)
	}
	if !gotType.Equals(capB) || gotType.Equals(capA) {
		t.Fatalf("expected the registry-held type B, got %#v", gotType)
	}
}

func TestCapsuleRegistriesAreIsolated(t *testing.T) {
	capA, capB := distinctCapsules()

	reg1 := NewCapsuleTypeRegistry()
	reg2 := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg1, capA, "shared/capsule", 1, equatableEncode, equatableDecode)
	mustRegisterCapsule(t, reg2, capB, "shared/capsule", 1, equatableEncode, equatableDecode)

	valA := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "iso"})
	valB := cty.CapsuleVal(capB, &equatableCapsuleNative{Text: "iso"})

	buf1, err := reg1.Marshal(valA, capA)
	if err != nil {
		t.Fatalf("unexpected Marshal error: %s", err)
	}
	buf2, err := reg2.Marshal(valB, capB)
	if err != nil {
		t.Fatalf("unexpected Marshal error: %s", err)
	}
	if string(buf1) != string(buf2) {
		t.Fatalf("the same wire identity should produce the same bytes\nbuf1: %s\nbuf2: %s", buf1, buf2)
	}

	got1, err := reg1.Unmarshal(buf1, capA)
	if err != nil {
		t.Fatalf("unexpected reg1 Unmarshal error: %s", err)
	}
	got2, err := reg2.Unmarshal(buf1, capB)
	if err != nil {
		t.Fatalf("unexpected reg2 Unmarshal error: %s", err)
	}
	if !got1.Type().Equals(capA) || !got2.Type().Equals(capB) {
		t.Fatalf("each registry should recover its own type identity\ngot1: %#v\ngot2: %#v", got1.Type(), got2.Type())
	}
	if !got1.RawEquals(valA) || !got2.RawEquals(valB) {
		t.Fatalf("values should compare equal within their own registry")
	}

	// A third, empty registry knows nothing about the wire identity.
	reg3 := NewCapsuleTypeRegistry()
	if _, err := reg3.Unmarshal(buf1, capA); err == nil {
		t.Fatalf("expected unregistered capsule type error from an isolated registry")
	}

	// The same bytes embedded in a dynamic wrapper resolve to each
	// registry's own type, proving no process-global type table is used.
	dynBuf1, err := reg1.Marshal(valA, cty.DynamicPseudoType)
	if err != nil {
		t.Fatalf("unexpected dynamic Marshal error: %s", err)
	}
	dynGot2, err := reg2.Unmarshal(dynBuf1, cty.DynamicPseudoType)
	if err != nil {
		t.Fatalf("unexpected dynamic Unmarshal error: %s", err)
	}
	if !dynGot2.Type().Equals(capB) {
		t.Fatalf("isolated registry should recover its own type from the dynamic type descriptor, got %#v", dynGot2.Type())
	}
}

func TestCapsuleRegistrationValidation(t *testing.T) {
	capA, capB := distinctCapsules()

	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/a", 1, equatableEncode, equatableDecode)

	tests := []struct {
		name string
		ty   cty.Type
		id   string
		ver  uint64
		enc  CapsuleValueEncoder
		dec  CapsuleValueDecoder
		want string
	}{
		{"not a capsule", cty.String, "example.com/string", 1, equatableEncode, equatableDecode, "only register capsule types"},
		{"empty id", capA, "", 1, equatableEncode, equatableDecode, "must not be empty"},
		{"zero version", capA, "example.com/zero", 0, equatableEncode, equatableDecode, "positive integer"},
		{"nil encoder", capA, "example.com/noenc", 1, nil, equatableDecode, "both an encode and a decode callback"},
		{"nil decoder", capA, "example.com/nodec", 1, equatableEncode, nil, "both an encode and a decode callback"},
		{"type bound twice", capA, "example.com/a2", 1, equatableEncode, equatableDecode, "already registered"},
		{"wire identity bound twice", capB, "example.com/a", 1, equatableEncode, equatableDecode, "already registered for a different cty type"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := reg.RegisterCapsuleType(test.ty, test.id, test.ver, test.enc, test.dec)
			if err == nil {
				t.Fatalf("expected registration error containing %q", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("registration error %q does not contain %q", err.Error(), test.want)
			}
		})
	}

	// Re-registering the exact same binding is a harmless no-op.
	if err := reg.RegisterCapsuleType(capA, "example.com/a", 1, equatableEncode, equatableDecode); err != nil {
		t.Fatalf("identical re-registration should be accepted: %s", err)
	}
}

func TestCapsuleTypeDescriptions(t *testing.T) {
	capA, _ := distinctCapsules()
	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/capsule", 1, equatableEncode, equatableDecode)

	t.Run("capsule descriptor", func(t *testing.T) {
		buf, err := reg.MarshalType(capA)
		if err != nil {
			t.Fatalf("unexpected MarshalType error: %s", err)
		}
		want := `{"capsule":{"id":"example.com/capsule","version":1}}`
		if string(buf) != want {
			t.Fatalf("wrong type descriptor\ngot:  %s\nwant: %s", buf, want)
		}
		got, err := reg.UnmarshalType(buf)
		if err != nil {
			t.Fatalf("unexpected UnmarshalType error: %s", err)
		}
		if !got.Equals(capA) {
			t.Fatalf("expected the registered type identity, got %#v", got)
		}
	})

	nestedTypes := []struct {
		name string
		ty   cty.Type
		want string
	}{
		{"list", cty.List(capA), `["list",{"capsule":{"id":"example.com/capsule","version":1}}]`},
		{"set", cty.Set(capA), `["set",{"capsule":{"id":"example.com/capsule","version":1}}]`},
		{"map", cty.Map(capA), `["map",{"capsule":{"id":"example.com/capsule","version":1}}]`},
		{"tuple", cty.Tuple([]cty.Type{cty.String, capA, cty.Bool}), `["tuple",["string",{"capsule":{"id":"example.com/capsule","version":1}},"bool"]]`},
		{"object", cty.Object(map[string]cty.Type{"a": cty.String, "b": capA}), `["object",{"a":"string","b":{"capsule":{"id":"example.com/capsule","version":1}}}]`},
		{"list of map", cty.List(cty.Map(capA)), `["list",["map",{"capsule":{"id":"example.com/capsule","version":1}}]]`},
		{"object with optional capsule", cty.ObjectWithOptionalAttrs(map[string]cty.Type{"cap": capA, "s": cty.String}, []string{"cap"}), `["object",{"cap":{"capsule":{"id":"example.com/capsule","version":1}},"s":"string"},["cap"]]`},
	}
	for _, test := range nestedTypes {
		t.Run(test.name, func(t *testing.T) {
			buf, err := reg.MarshalType(test.ty)
			if err != nil {
				t.Fatalf("unexpected MarshalType error: %s", err)
			}
			if string(buf) != test.want {
				t.Fatalf("wrong descriptor\ngot:  %s\nwant: %s", buf, test.want)
			}
			got, err := reg.UnmarshalType(buf)
			if err != nil {
				t.Fatalf("unexpected UnmarshalType error: %s", err)
			}
			if !got.Equals(test.ty) {
				t.Fatalf("type mismatch after round trip\ngot:  %#v\nwant: %#v", got, test.ty)
			}
			if test.name == "object with optional capsule" {
				optionals := got.OptionalAttributes()
				if _, ok := optionals["cap"]; !ok {
					t.Fatalf("optional attribute marker lost: %#v", optionals)
				}
				if attrTy := got.AttributeTypes()["cap"]; !attrTy.Equals(capA) {
					t.Fatalf("optional capsule attribute lost its registered identity: %#v", attrTy)
				}
			}
		})
	}

	t.Run("unregistered capsule type cannot be described", func(t *testing.T) {
		if _, err := reg.MarshalType(cty.Capsule("other", reflect.TypeOf(plainCapsuleNative{}))); err == nil {
			t.Fatalf("expected error describing an unregistered capsule type")
		}
	})

	t.Run("bad type descriptors", func(t *testing.T) {
		descs := []struct {
			buf  string
			want string
		}{
			{`{"capsule":{"id":"example.com/capsule"}}`, "positive format version"},
			{`{"capsule":{"id":"example.com/missing","version":1}}`, `no capsule type registered with id "example.com/missing"`},
			{`{"capsule":{"id":"example.com/capsule","version":2}}`, "unsupported format version 2"},
			{`{"capsule":{}}`, "missing its id"},
			{`{"other":{}}`, "unknown field"},
			{`[]`, "invalid complex type description"},
			{`["object",{"a":"string"},["b"]]`, `optional attribute "b" is not part of the object type`},
			{`["bogus","string"]`, `invalid complex type kind name "bogus"`},
		}
		for _, desc := range descs {
			t.Run(desc.want, func(t *testing.T) {
				_, err := reg.UnmarshalType([]byte(desc.buf))
				if err == nil {
					t.Fatalf("expected error containing %q for %s", desc.want, desc.buf)
				}
				if !strings.Contains(err.Error(), desc.want) {
					t.Fatalf("error %q does not contain %q", err.Error(), desc.want)
				}
			})
		}
	})

	t.Run("default JSON type API still rejects capsules", func(t *testing.T) {
		if _, err := capA.MarshalJSON(); err == nil {
			t.Fatalf("Type.MarshalJSON should still reject capsule types")
		}
		if _, err := MarshalType(capA); err == nil {
			t.Fatalf("package-level MarshalType should still reject capsule types")
		}
		var fallback cty.Type
		if err := fallback.UnmarshalJSON([]byte(`{"capsule":{"id":"example.com/capsule","version":1}}`)); err == nil {
			t.Fatalf("Type.UnmarshalJSON should still reject capsule descriptors")
		}
		if _, err := UnmarshalType([]byte(`{"capsule":{"id":"example.com/capsule","version":1}}`)); err == nil {
			t.Fatalf("package-level UnmarshalType should still reject capsule descriptors")
		}
	})

	t.Run("default value API keeps its legacy capsule behavior", func(t *testing.T) {
		orig := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "legacy"})
		buf, err := Marshal(orig, capA)
		if err != nil {
			t.Fatalf("unexpected legacy Marshal error: %s", err)
		}
		if string(buf) != `{"text":"legacy"}` {
			t.Fatalf("legacy capsule marshal should not use an envelope: %s", buf)
		}
		got, err := Unmarshal(buf, capA)
		if err != nil {
			t.Fatalf("unexpected legacy Unmarshal error: %s", err)
		}
		if !got.Type().Equals(capA) {
			t.Fatalf("legacy round trip changed type: %#v", got.Type())
		}
	})
}

func TestCapsuleDynamicValues(t *testing.T) {
	capA, _ := distinctCapsules()
	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/capsule", 1, equatableEncode, equatableDecode)

	t.Run("capsule as a dynamic value", func(t *testing.T) {
		orig := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "dyn"})
		buf, err := reg.Marshal(orig, cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		want := `{"value":{"id":"example.com/capsule","version":1,"value":{"text":"dyn"}},"type":{"capsule":{"id":"example.com/capsule","version":1}}}`
		if string(buf) != want {
			t.Fatalf("wrong dynamic encoding\ngot:  %s\nwant: %s", buf, want)
		}
		got, err := reg.Unmarshal(buf, cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.Type().Equals(capA) || !got.RawEquals(orig) {
			t.Fatalf("dynamic round trip mismatch: %#v", got)
		}
	})

	t.Run("nested dynamic list with capsule, string and typed null", func(t *testing.T) {
		// Each tuple element is independently typed as dynamic, so the
		// concrete capsule, string and null types all travel with their
		// own type descriptors.
		dynTupleTy := cty.Tuple([]cty.Type{
			cty.DynamicPseudoType,
			cty.DynamicPseudoType,
			cty.DynamicPseudoType,
		})
		orig := cty.TupleVal([]cty.Value{
			cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "nested"}),
			cty.StringVal("plain"),
			cty.NullVal(capA),
		})
		buf, err := reg.Marshal(orig, dynTupleTy)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		got, err := reg.Unmarshal(buf, dynTupleTy)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		elems := got.AsValueSlice()
		if !elems[0].Type().Equals(capA) {
			t.Fatalf("element 0 should be the registered capsule: %#v", elems[0].Type())
		}
		if !elems[0].RawEquals(cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "nested"})) {
			t.Fatalf("capsule element content lost: %#v", elems[0])
		}
		if elems[1].Type() != cty.String || elems[1].AsString() != "plain" {
			t.Fatalf("element 1 should be a string: %#v", elems[1])
		}
		if !elems[2].IsNull() || !elems[2].Type().Equals(capA) {
			t.Fatalf("element 2 should be a typed null capsule: %#v", elems[2])
		}
	})

	t.Run("dynamic attribute containing a capsule inside an object", func(t *testing.T) {
		objTy := cty.Object(map[string]cty.Type{
			"d": cty.DynamicPseudoType,
			"s": cty.String,
		})
		orig := cty.ObjectVal(map[string]cty.Value{
			"d": cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "attr"}),
			"s": cty.StringVal("keep"),
		})
		buf, err := reg.Marshal(orig, objTy)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		got, err := reg.Unmarshal(buf, objTy)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		d := got.GetAttr("d")
		if !d.Type().Equals(capA) || !d.RawEquals(orig.GetAttr("d")) {
			t.Fatalf("dynamic attribute did not round trip the capsule: %#v", d)
		}
	})

	t.Run("dynamic value whose type is an object containing a capsule", func(t *testing.T) {
		orig := cty.ObjectVal(map[string]cty.Value{
			"c": cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "deep"}),
		})
		buf, err := reg.Marshal(orig, cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		got, err := reg.Unmarshal(buf, cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.Type().IsObjectType() {
			t.Fatalf("expected object type, got %#v", got.Type())
		}
		if c := got.GetAttr("c"); !c.Type().Equals(capA) || !c.RawEquals(orig.GetAttr("c")) {
			t.Fatalf("nested capsule did not survive dynamic round trip: %#v", c)
		}
	})

	t.Run("unknown dynamic value cannot be encoded", func(t *testing.T) {
		if _, err := reg.Marshal(cty.DynamicVal, cty.DynamicPseudoType); err == nil {
			t.Fatalf("expected error encoding an unknown dynamic value")
		}
	})

	t.Run("unregistered capsule in dynamic descriptor", func(t *testing.T) {
		buf := []byte(`{"value":1,"type":{"capsule":{"id":"example.com/nope","version":1}}}`)
		_, err := reg.Unmarshal(buf, cty.DynamicPseudoType)
		pe := requirePathError(t, err, `no capsule type registered with id "example.com/nope"`)
		if len(pe.Path) != 0 {
			t.Fatalf("top-level dynamic error should have an empty path, got %#v", pe.Path)
		}

		// Inside a list of dynamic values the error path must point at
		// the offending element.
		nested := []byte(`[` + string(buf) + `,{"value":true,"type":"bool"}]`)
		_, err = reg.Unmarshal(nested, cty.List(cty.DynamicPseudoType))
		pe = requirePathError(t, err, `no capsule type registered with id "example.com/nope"`)
		if len(pe.Path) != 1 {
			t.Fatalf("expected a single index step, got %#v", pe.Path)
		}
		step, ok := pe.Path[0].(cty.IndexStep)
		if !ok || !step.Key.RawEquals(cty.NumberIntVal(0)) {
			t.Fatalf("expected index 0, got %#v", pe.Path[0])
		}
	})
}

func TestCapsuleNullUnknownAndMarks(t *testing.T) {
	capA, _ := distinctCapsules()
	calls := 0
	countingDecode := func(raw json.RawMessage) (any, error) {
		calls++
		return equatableDecode(raw)
	}

	reg := NewCapsuleTypeRegistry()
	if err := reg.RegisterCapsuleType(capA, "example.com/capsule", 1, equatableEncode, countingDecode); err != nil {
		t.Fatalf("registration failed: %s", err)
	}

	t.Run("typed null does not invoke the decoder", func(t *testing.T) {
		calls = 0
		buf, err := reg.Marshal(cty.NullVal(capA), capA)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		if string(buf) != "null" {
			t.Fatalf("typed null should serialize as bare null, got %s", buf)
		}
		got, err := reg.Unmarshal(buf, capA)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.IsNull() || !got.Type().Equals(capA) {
			t.Fatalf("expected a typed null capsule, got %#v", got)
		}
		if calls != 0 {
			t.Fatalf("decode callback should not be called for null, was called %d times", calls)
		}
	})

	t.Run("typed null inside a dynamic wrapper does not invoke the decoder", func(t *testing.T) {
		calls = 0
		buf, err := reg.Marshal(cty.NullVal(capA), cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		got, err := reg.Unmarshal(buf, cty.DynamicPseudoType)
		if err != nil {
			t.Fatalf("unexpected Unmarshal error: %s", err)
		}
		if !got.IsNull() || !got.Type().Equals(capA) {
			t.Fatalf("expected a typed null capsule, got %#v", got)
		}
		if calls != 0 {
			t.Fatalf("decode callback should not be called for dynamic null, was called %d times", calls)
		}
	})

	t.Run("unknown values cannot be encoded", func(t *testing.T) {
		_, err := reg.Marshal(cty.UnknownVal(capA), capA)
		requirePathError(t, err, "value is not known")
	})

	t.Run("marked values are rejected, not unmarked", func(t *testing.T) {
		marked := cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "secret"}).Mark("sensitive")
		_, err := reg.Marshal(marked, capA)
		requirePathError(t, err, "value has marks")
		if !marked.IsMarked() {
			t.Fatalf("marshalling must not strip marks from the input value")
		}

		// Nested marks surface as path errors as well.
		list := cty.ListVal([]cty.Value{
			cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "ok"}),
			cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "secret"}).Mark("sensitive"),
		})
		pe := requirePathError(t, func() error {
			_, err := reg.Marshal(list, cty.List(capA))
			return err
		}(), "value has marks")
		if len(pe.Path) != 1 {
			t.Fatalf("expected path to the marked element, got %#v", pe.Path)
		}
	})
}

func TestCapsuleDecodeFailures(t *testing.T) {
	capA, capB := distinctCapsules()
	capFail := cty.Capsule("fail-capsule", reflect.TypeOf(equatableCapsuleNative{}))
	capPanic := cty.Capsule("panic-capsule", reflect.TypeOf(equatableCapsuleNative{}))
	capEncPanic := cty.Capsule("encpanic-capsule", reflect.TypeOf(equatableCapsuleNative{}))

	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/a", 1, equatableEncode, equatableDecode)
	mustRegisterCapsule(t, reg, capB, "example.com/b", 1, equatableEncode, equatableDecode)
	mustRegisterCapsule(t, reg, capFail, "example.com/fail", 1, equatableEncode, func(raw json.RawMessage) (any, error) {
		var probe struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(raw, &probe)
		if probe.Text == "boom" {
			return nil, fmt.Errorf("refusing payload %q", probe.Text)
		}
		return equatableDecode(raw)
	})
	mustRegisterCapsule(t, reg, capPanic, "example.com/panic", 1, equatableEncode, func(json.RawMessage) (any, error) {
		panic("decoder panic")
	})
	mustRegisterCapsule(t, reg, capEncPanic, "example.com/encpanic", 1, func(any) (json.RawMessage, error) {
		panic("encoder panic")
	}, equatableDecode)

	envelope := func(id string, version uint64, value string) []byte {
		return []byte(fmt.Sprintf(`{"id":%q,"version":%d,"value":%s}`, id, version, value))
	}

	t.Run("unregistered id", func(t *testing.T) {
		buf := envelope("example.com/missing", 1, `{}`)
		_, err := reg.Unmarshal(buf, capA)
		requirePathError(t, err, `no capsule type registered with id "example.com/missing"`)
	})

	t.Run("unknown version", func(t *testing.T) {
		buf := envelope("example.com/a", 2, `{"text":"x"}`)
		_, err := reg.Unmarshal(buf, capA)
		requirePathError(t, err, "unsupported format version 2")
	})

	t.Run("descriptor does not match target type", func(t *testing.T) {
		buf := envelope("example.com/a", 1, `{"text":"x"}`)
		_, err := reg.Unmarshal(buf, capB)
		pe := requirePathError(t, err, "does not match the target type")
		if len(pe.Path) != 0 {
			t.Fatalf("mismatch should be reported at the capsule value itself")
		}
	})

	t.Run("decoder error at accurate nested path", func(t *testing.T) {
		list := cty.ListVal([]cty.Value{
			cty.CapsuleVal(capFail, &equatableCapsuleNative{Text: "ok"}),
			cty.CapsuleVal(capFail, &equatableCapsuleNative{Text: "boom"}),
		})
		buf, err := reg.Marshal(list, cty.List(capFail))
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		_, err = reg.Unmarshal(buf, cty.List(capFail))
		pe := requirePathError(t, err, `refusing payload "boom"`)
		if len(pe.Path) != 1 {
			t.Fatalf("expected one index step, got %#v", pe.Path)
		}
		step, ok := pe.Path[0].(cty.IndexStep)
		if !ok || !step.Key.RawEquals(cty.NumberIntVal(1)) {
			t.Fatalf("expected error path index 1, got %#v", pe.Path[0])
		}
	})

	t.Run("decoder error under an object attribute", func(t *testing.T) {
		obj := cty.ObjectVal(map[string]cty.Value{
			"ok":  cty.CapsuleVal(capA, &equatableCapsuleNative{Text: "fine"}),
			"bad": cty.CapsuleVal(capFail, &equatableCapsuleNative{Text: "boom"}),
		})
		objTy := cty.Object(map[string]cty.Type{"ok": capA, "bad": capFail})
		buf, err := reg.Marshal(obj, objTy)
		if err != nil {
			t.Fatalf("unexpected Marshal error: %s", err)
		}
		_, err = reg.Unmarshal(buf, objTy)
		pe := requirePathError(t, err, `refusing payload "boom"`)
		if len(pe.Path) != 1 {
			t.Fatalf("expected one attribute step, got %#v", pe.Path)
		}
		step, ok := pe.Path[0].(cty.GetAttrStep)
		if !ok || step.Name != "bad" {
			t.Fatalf("expected attribute bad, got %#v", pe.Path[0])
		}
	})

	t.Run("decoder panic becomes a path error", func(t *testing.T) {
		buf := envelope("example.com/panic", 1, `{"text":"x"}`)
		_, err := reg.Unmarshal(buf, capPanic)
		requirePathError(t, err, "decoder panic")
	})

	t.Run("encoder panic becomes a path error", func(t *testing.T) {
		val := cty.CapsuleVal(capEncPanic, &equatableCapsuleNative{Text: "x"})
		_, err := reg.Marshal(val, capEncPanic)
		requirePathError(t, err, "encoder panic")
	})

	t.Run("invalid callback return values", func(t *testing.T) {
		cases := []struct {
			name string
			dec  CapsuleValueDecoder
			want string
		}{
			{
				"nil interface",
				func(json.RawMessage) (any, error) { return nil, nil },
				"returned nil",
			},
			{
				"typed nil pointer",
				func(json.RawMessage) (any, error) { return (*equatableCapsuleNative)(nil), nil },
				"nil pointer",
			},
			{
				"non-pointer",
				func(json.RawMessage) (any, error) { return equatableCapsuleNative{Text: "x"}, nil },
				"not a pointer",
			},
			{
				"wrong native pointer type",
				func(json.RawMessage) (any, error) { return &plainCapsuleNative{N: 1}, nil },
				"not compatible with the native type",
			},
		}
		for _, test := range cases {
			t.Run(test.name, func(t *testing.T) {
				badTy := cty.Capsule("badreturn-"+test.name, reflect.TypeOf(equatableCapsuleNative{}))
				localReg := NewCapsuleTypeRegistry()
				mustRegisterCapsule(t, localReg, badTy, "example.com/badreturn", 1, equatableEncode, test.dec)
				buf := envelope("example.com/badreturn", 1, `{"text":"x"}`)
				_, err := localReg.Unmarshal(buf, badTy)
				requirePathError(t, err, test.want)
			})
		}
	})

	t.Run("malformed envelopes", func(t *testing.T) {
		cases := []struct {
			buf  string
			want string
		}{
			{`[1,2]`, "failed to decode capsule value descriptor"},
			{`"string"`, "failed to decode capsule value descriptor"},
			{`{"version":1,"value":{}}`, "missing its id"},
			{`{"id":"example.com/a","value":{}}`, "positive format version"},
			{`{"id":"example.com/a","version":1}`, "missing its value"},
			{`{"id":"example.com/a","version":1,"value":null}`, "bare JSON null"},
			{`{"id":"example.com/a","version":1,"value":{"text":"x"},"extra":true}`, "unknown field"},
			{`{"id":"example.com/a","version":1,"value":{"text":"x"}}trailing`, "invalid character"},
			{`{"id":"example.com/a","version":1,"value":{"text":`, "failed to decode capsule value descriptor"},
		}
		for _, test := range cases {
			t.Run(test.want, func(t *testing.T) {
				_, err := reg.Unmarshal([]byte(test.buf), capA)
				requirePathError(t, err, test.want)
			})
		}
	})

	t.Run("invalid encoder output", func(t *testing.T) {
		badTy := cty.Capsule("badjson", reflect.TypeOf(equatableCapsuleNative{}))
		localReg := NewCapsuleTypeRegistry()
		mustRegisterCapsule(t, localReg, badTy, "example.com/badjson", 1, func(any) (json.RawMessage, error) {
			return json.RawMessage(`{not json`), nil
		}, equatableDecode)
		val := cty.CapsuleVal(badTy, &equatableCapsuleNative{Text: "x"})
		_, err := localReg.Marshal(val, badTy)
		requirePathError(t, err, "returned invalid JSON")
	})

	t.Run("unregistered target type", func(t *testing.T) {
		other := cty.Capsule("other", reflect.TypeOf(plainCapsuleNative{}))
		val := cty.CapsuleVal(other, &plainCapsuleNative{N: 1})
		if _, err := reg.Marshal(val, other); err == nil {
			t.Fatalf("expected error marshalling with an unregistered capsule type")
		}
		if _, err := reg.Unmarshal([]byte(`null`), other); err != nil {
			t.Fatalf("bare null should decode as a typed null without registration: %s", err)
		}
	})
}

func TestCapsuleOptionalAttributeTypeRoundTrip(t *testing.T) {
	capA, _ := distinctCapsules()
	reg := NewCapsuleTypeRegistry()
	mustRegisterCapsule(t, reg, capA, "example.com/capsule", 1, equatableEncode, equatableDecode)

	ty := cty.ObjectWithOptionalAttrs(map[string]cty.Type{
		"cap": capA,
		"s":   cty.String,
	}, []string{"cap"})

	// Missing optional attribute is filled in as a typed null, without the
	// decode callback being consulted.
	buf, err := reg.Marshal(cty.ObjectVal(map[string]cty.Value{
		"cap": cty.NullVal(capA),
		"s":   cty.StringVal("present"),
	}), ty)
	if err != nil {
		t.Fatalf("unexpected Marshal error: %s", err)
	}
	got, err := reg.Unmarshal(buf, ty)
	if err != nil {
		t.Fatalf("unexpected Unmarshal error: %s", err)
	}
	cap := got.GetAttr("cap")
	if !cap.IsNull() || !cap.Type().Equals(capA) {
		t.Fatalf("expected typed null capsule in optional attribute, got %#v", cap)
	}
}
