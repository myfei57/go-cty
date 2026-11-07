package json

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"

	"github.com/zclconf/go-cty/cty"
)

// CapsuleValueEncoder encodes the native pointer encapsulated in a known,
// non-null capsule value into a JSON representation.
//
// The argument is the same value that was passed to cty.CapsuleVal: a
// pointer to the capsule type's native value. The returned JSON must be a
// single complete JSON value; it is embedded verbatim into the capsule
// value envelope.
//
// The encoder is never called for null or unknown capsule values, which are
// represented without consulting the registered callbacks.
type CapsuleValueEncoder func(native any) (json.RawMessage, error)

// CapsuleValueDecoder decodes the JSON representation of a known, non-null
// capsule value and returns a pointer to a newly-allocated value of the
// capsule type's native type.
//
// The returned pointer is checked against the native type of the capsule
// type registered by the registry being used. Returning a nil interface,
// a typed nil pointer, a non-pointer, or a pointer to an incompatible type
// all produce a cty.PathError, as does a panic from the callback itself.
type CapsuleValueDecoder func(raw json.RawMessage) (native any, err error)

// capsuleIdentityWire is the stable on-wire identity of a registered
// capsule type.
type capsuleIdentityWire struct {
	ID      string `json:"id"`
	Version uint64 `json:"version"`
}

// capsuleTypeWire is the JSON type description for a registered capsule
// type:
//
//	{"capsule":{"id":"example.com/MyType","version":1}}
type capsuleTypeWire struct {
	Capsule capsuleIdentityWire `json:"capsule"`
}

// capsuleValueWire is the JSON value envelope for a known, non-null
// registered capsule value:
//
//	{"id":"example.com/MyType","version":1,"value":{...}}
//
// Null capsule values are encoded as a bare JSON null instead, and unknown
// values cannot be encoded as JSON at all.
type capsuleValueWire struct {
	ID      string          `json:"id"`
	Version uint64          `json:"version"`
	Value   json.RawMessage `json:"value"`
}

type capsuleEntry struct {
	ty      cty.Type
	id      string
	version uint64
	encode  CapsuleValueEncoder
	decode  CapsuleValueDecoder
}

// CapsuleTypeRegistry is an explicit, caller-owned mapping between cty
// capsule types and stable JSON identities consisting of a namespace ID
// string and a positive integer format version.
//
// Registries are not connected to any process-global type table: the
// bindings known to one registry are invisible to every other registry,
// and two registries may bind the same wire identity to different cty
// capsule types. Capsule types are compared by their cty.Type identity,
// never by friendly name or reflected Go type; decoding always yields the
// same cty.Type value that was originally registered, rather than a freshly
// constructed capsule type that happens to share a name.
//
// The zero value is not ready for use; create registries with
// NewCapsuleTypeRegistry.
type CapsuleTypeRegistry struct {
	// byID maps namespace IDs to the versions registered for that ID.
	byID map[string]map[uint64]*capsuleEntry

	// byType maps registered capsule types to their entries. The keys are
	// always cty capsule types, whose typeImpl is a pointer and which are
	// therefore safe to use as (and compare through) map keys.
	byType map[cty.Type]*capsuleEntry
}

// NewCapsuleTypeRegistry creates a new empty capsule type registry.
//
// A registry carries no state beyond the bindings registered on it and does
// not interact with any global registry, so independent callers (or tests)
// can use isolated registries without interfering with one another.
func NewCapsuleTypeRegistry() *CapsuleTypeRegistry {
	return &CapsuleTypeRegistry{
		byID:   map[string]map[uint64]*capsuleEntry{},
		byType: map[cty.Type]*capsuleEntry{},
	}
}

// RegisterCapsuleType binds the given capsule type to a stable JSON
// identity described by a namespace ID and a positive integer format
// version, along with the callbacks used to encode and decode known,
// non-null values of that type.
//
// The same ID/version pair may not be bound to two different cty types,
// and a single cty type may not be bound to two different ID/version
// pairs. Type identity is taken from the cty type system: two separately
// constructed capsule types conflict even if their friendly names and
// reflected native Go types are identical. Re-registering the exact same
// type with the exact same ID and version is accepted as a no-op.
//
// There is no automatic migration between versions: decoders accept only
// versions explicitly registered and unknown versions are rejected.
func (r *CapsuleTypeRegistry) RegisterCapsuleType(t cty.Type, id string, version uint64, encode CapsuleValueEncoder, decode CapsuleValueDecoder) error {
	if !t.IsCapsuleType() {
		return fmt.Errorf("can only register capsule types for JSON serialization, given %s", t.FriendlyName())
	}
	if id == "" {
		return fmt.Errorf("capsule type namespace ID must not be empty")
	}
	if version == 0 {
		return fmt.Errorf("capsule type format version for %q must be a positive integer", id)
	}
	if encode == nil || decode == nil {
		return fmt.Errorf("capsule type %q version %d requires both an encode and a decode callback", id, version)
	}

	if versions, ok := r.byID[id]; ok {
		if existing, ok := versions[version]; ok {
			if !existing.ty.Equals(t) {
				return fmt.Errorf("capsule type identity %q version %d is already registered for a different cty type", id, version)
			}
			// Exact same binding registered again: treat as a no-op so
			// package-level setup can run more than once.
			return nil
		}
	}
	if existing, ok := r.byType[t]; ok {
		return fmt.Errorf("capsule type %s is already registered as %q version %d", t.FriendlyName(), existing.id, existing.version)
	}

	entry := &capsuleEntry{
		ty:      t,
		id:      id,
		version: version,
		encode:  encode,
		decode:  decode,
	}
	if r.byID[id] == nil {
		r.byID[id] = map[uint64]*capsuleEntry{}
	}
	r.byID[id][version] = entry
	r.byType[t] = entry
	return nil
}

// lookupEntry returns the entry for the given wire identity, distinguishing
// between an unregistered ID and a registered ID whose given version is not
// known.
func (r *CapsuleTypeRegistry) lookupEntry(id string, version uint64) (*capsuleEntry, error) {
	versions, ok := r.byID[id]
	if !ok {
		return nil, fmt.Errorf("no capsule type registered with id %q", id)
	}
	entry, ok := versions[version]
	if !ok {
		known := make([]uint64, 0, len(versions))
		for v := range versions {
			known = append(known, v)
		}
		sort.Slice(known, func(i, j int) bool { return known[i] < known[j] })
		return nil, fmt.Errorf("unsupported format version %d for capsule type %q; registered versions are %v", version, id, known)
	}
	return entry, nil
}

// MarshalType serializes the given type as JSON, understanding capsule
// types previously registered on the receiver.
//
// List, map, set, tuple and object types are handled recursively, so
// registered capsules may appear at any nesting depth, including as
// optional object attribute types. This does not change the behavior of
// the package-level MarshalType function or cty.Type.MarshalJSON, both of
// which continue to reject capsule types.
func (r *CapsuleTypeRegistry) MarshalType(t cty.Type) ([]byte, error) {
	return r.marshalType(t)
}

// UnmarshalType decodes a JSON type description produced by MarshalType on
// a registry with the same capsule bindings.
//
// Capsule types in the description are resolved to the exact cty.Type
// values held by the receiver; no capsule type is ever reconstructed from
// its name or native Go type. Unregistered IDs, unknown format versions,
// and malformed descriptions produce errors.
func (r *CapsuleTypeRegistry) UnmarshalType(buf []byte) (cty.Type, error) {
	return r.unmarshalType(buf)
}

func (r *CapsuleTypeRegistry) marshalType(t cty.Type) ([]byte, error) {
	switch {
	case t == cty.DynamicPseudoType:
		return json.Marshal("dynamic")
	case t.IsPrimitiveType():
		return json.Marshal(t.FriendlyName())
	case t.IsListType(), t.IsMapType(), t.IsSetType():
		kind := ""
		switch {
		case t.IsListType():
			kind = "list"
		case t.IsMapType():
			kind = "map"
		case t.IsSetType():
			kind = "set"
		}
		etyJSON, err := r.marshalType(t.ElementType())
		if err != nil {
			return nil, err
		}
		return json.Marshal([]json.RawMessage{rawTypeKind(kind), etyJSON})
	case t.IsTupleType():
		etys := t.TupleElementTypes()
		elems := make([]json.RawMessage, 0, len(etys))
		for _, ety := range etys {
			etyJSON, err := r.marshalType(ety)
			if err != nil {
				return nil, err
			}
			elems = append(elems, etyJSON)
		}
		elemsJSON, err := json.Marshal(elems)
		if err != nil {
			return nil, err
		}
		return json.Marshal([]json.RawMessage{rawTypeKind("tuple"), elemsJSON})
	case t.IsObjectType():
		atys := t.AttributeTypes()
		names := make([]string, 0, len(atys))
		for name := range atys {
			names = append(names, name)
		}
		sort.Strings(names)

		attrs := make(map[string]json.RawMessage, len(atys))
		for _, name := range names {
			atyJSON, err := r.marshalType(atys[name])
			if err != nil {
				return nil, err
			}
			attrs[name] = atyJSON
		}
		attrsJSON, err := json.Marshal(attrs)
		if err != nil {
			return nil, err
		}

		parts := []json.RawMessage{rawTypeKind("object"), attrsJSON}

		if optionals := t.OptionalAttributes(); len(optionals) > 0 {
			optionalNames := make([]string, 0, len(optionals))
			for name := range optionals {
				optionalNames = append(optionalNames, name)
			}
			sort.Strings(optionalNames)
			optionalJSON, err := json.Marshal(optionalNames)
			if err != nil {
				return nil, err
			}
			parts = append(parts, optionalJSON)
		}

		return json.Marshal(parts)
	case t.IsCapsuleType():
		entry, ok := r.byType[t]
		if !ok {
			return nil, fmt.Errorf("capsule type %s is not registered for JSON serialization", t.FriendlyName())
		}
		return json.Marshal(capsuleTypeWire{
			Capsule: capsuleIdentityWire{
				ID:      entry.id,
				Version: entry.version,
			},
		})
	default:
		return nil, fmt.Errorf("cannot JSON-serialize type %s", t.FriendlyName())
	}
}

func rawTypeKind(kind string) json.RawMessage {
	buf, err := json.Marshal(kind)
	if err != nil {
		// Only ever called with fixed kind strings.
		panic(err)
	}
	return json.RawMessage(buf)
}

func (r *CapsuleTypeRegistry) unmarshalType(buf []byte) (cty.Type, error) {
	trimmed := bytes.TrimSpace(buf)
	if len(trimmed) == 0 {
		return cty.NilType, errors.New("empty type description")
	}

	switch trimmed[0] {
	case '"':
		var name string
		if err := json.Unmarshal(trimmed, &name); err != nil {
			return cty.NilType, err
		}
		switch name {
		case "bool":
			return cty.Bool, nil
		case "number":
			return cty.Number, nil
		case "string":
			return cty.String, nil
		case "dynamic":
			return cty.DynamicPseudoType, nil
		default:
			return cty.NilType, fmt.Errorf("invalid primitive type name %q", name)
		}
	case '[':
		var parts []json.RawMessage
		if err := json.Unmarshal(trimmed, &parts); err != nil {
			return cty.NilType, err
		}
		if len(parts) < 2 {
			return cty.NilType, errors.New("invalid complex type description")
		}

		var kind string
		if err := json.Unmarshal(parts[0], &kind); err != nil {
			return cty.NilType, fmt.Errorf("invalid complex type kind name: %s", err)
		}

		switch kind {
		case "list", "map", "set":
			if len(parts) != 2 {
				return cty.NilType, fmt.Errorf("invalid %s type description", kind)
			}
			ety, err := r.unmarshalType(parts[1])
			if err != nil {
				return cty.NilType, err
			}
			switch kind {
			case "list":
				return cty.List(ety), nil
			case "map":
				return cty.Map(ety), nil
			default:
				return cty.Set(ety), nil
			}
		case "tuple":
			if len(parts) != 2 {
				return cty.NilType, errors.New("invalid tuple type description")
			}
			var elems []json.RawMessage
			if err := json.Unmarshal(parts[1], &elems); err != nil {
				return cty.NilType, err
			}
			etys := make([]cty.Type, 0, len(elems))
			for _, elem := range elems {
				ety, err := r.unmarshalType(elem)
				if err != nil {
					return cty.NilType, err
				}
				etys = append(etys, ety)
			}
			return cty.Tuple(etys), nil
		case "object":
			if len(parts) != 2 && len(parts) != 3 {
				return cty.NilType, errors.New("invalid object type description")
			}
			var attrs map[string]json.RawMessage
			if err := json.Unmarshal(parts[1], &attrs); err != nil {
				return cty.NilType, err
			}
			atys := make(map[string]cty.Type, len(attrs))
			for name, raw := range attrs {
				aty, err := r.unmarshalType(raw)
				if err != nil {
					return cty.NilType, err
				}
				atys[name] = aty
			}
			if len(parts) == 3 {
				var optional []string
				if err := json.Unmarshal(parts[2], &optional); err != nil {
					return cty.NilType, err
				}
				for _, name := range optional {
					if _, ok := atys[name]; !ok {
						return cty.NilType, fmt.Errorf("optional attribute %q is not part of the object type", name)
					}
				}
				return cty.ObjectWithOptionalAttrs(atys, optional), nil
			}
			return cty.Object(atys), nil
		default:
			return cty.NilType, fmt.Errorf("invalid complex type kind name %q", kind)
		}
	case '{':
		var wire capsuleTypeWire
		if err := decodeStrict(trimmed, &wire); err != nil {
			return cty.NilType, fmt.Errorf("invalid capsule type description: %s", err)
		}
		if wire.Capsule.ID == "" {
			return cty.NilType, errors.New("capsule type description is missing its id")
		}
		if wire.Capsule.Version == 0 {
			return cty.NilType, fmt.Errorf("capsule type description for %q is missing a positive format version", wire.Capsule.ID)
		}
		entry, err := r.lookupEntry(wire.Capsule.ID, wire.Capsule.Version)
		if err != nil {
			return cty.NilType, err
		}
		return entry.ty, nil
	default:
		return cty.NilType, errors.New("invalid type description")
	}
}

// Marshal serializes the given value as JSON against the given type, using
// the receiver's capsule bindings whenever a capsule type is encountered.
//
// Registered capsule values are encoded as an identity/value envelope and
// the registered encode callback is invoked for known, non-null values
// only. Null values retain their type without invoking the callback,
// unknown values cannot be encoded (matching the package-level Marshal
// contract), and marked values are rejected outright rather than having
// their marks stripped.
func (r *CapsuleTypeRegistry) Marshal(val cty.Value, t cty.Type) ([]byte, error) {
	return marshalValue(val, t, r)
}

// Unmarshal decodes JSON produced by Marshal against the given type, using
// the receiver's capsule bindings.
//
// The result uses the exact cty.Type registered on the receiver rather
// than a reconstructed type. Unregistered capsule IDs, unknown format
// versions, and capsule descriptors whose identity does not match the
// target type all produce cty.PathError values located at the offending
// value.
func (r *CapsuleTypeRegistry) Unmarshal(buf []byte, t cty.Type) (cty.Value, error) {
	var path cty.Path
	return unmarshal(buf, t, path, r)
}

// marshalCapsuleValue writes the envelope for a known, non-null registered
// capsule value. Null, unknown and marked values are handled (and rejected)
// by the caller before this point.
func (r *CapsuleTypeRegistry) marshalCapsuleValue(val cty.Value, t cty.Type, path cty.Path, b *bytes.Buffer) (err error) {
	entry, ok := r.byType[t]
	if !ok {
		return path.NewErrorf("capsule type %s is not registered for JSON serialization", t.FriendlyName())
	}

	var raw json.RawMessage
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("encode callback for capsule type %q version %d panicked: %v", entry.id, entry.version, recovered)
			}
		}()
		raw, err = entry.encode(val.EncapsulatedValue())
	}()
	if err != nil {
		return path.NewError(err)
	}
	if !json.Valid(raw) {
		return path.NewErrorf("encode callback for capsule type %q version %d returned invalid JSON", entry.id, entry.version)
	}

	idJSON, err := json.Marshal(entry.id)
	if err != nil {
		return path.NewError(err)
	}
	b.WriteString(`{"id":`)
	b.Write(idJSON)
	fmt.Fprintf(b, `,"version":%d,"value":`, entry.version)
	b.Write(raw)
	b.WriteRune('}')
	return nil
}

// unmarshalCapsuleValue decodes a registered capsule value envelope. The
// caller has already handled a bare JSON null, which represents a typed
// null.
func (r *CapsuleTypeRegistry) unmarshalCapsuleValue(buf []byte, want cty.Type, path cty.Path) (cty.Value, error) {
	if _, ok := r.byType[want]; !ok {
		return cty.NilVal, path.NewErrorf("capsule type %s is not registered for JSON serialization", want.FriendlyName())
	}

	var wire capsuleValueWire
	if err := decodeStrict(buf, &wire); err != nil {
		return cty.NilVal, path.NewErrorf("failed to decode capsule value descriptor: %s", err)
	}
	if wire.ID == "" {
		return cty.NilVal, path.NewErrorf("capsule value descriptor is missing its id")
	}
	if wire.Version == 0 {
		return cty.NilVal, path.NewErrorf("capsule value descriptor for %q is missing a positive format version", wire.ID)
	}
	if len(bytes.TrimSpace(wire.Value)) == 0 {
		return cty.NilVal, path.NewErrorf("capsule value descriptor for %q version %d is missing its value", wire.ID, wire.Version)
	}
	if string(bytes.TrimSpace(wire.Value)) == "null" {
		// A typed null travels as a bare JSON null so that no decode
		// callback is involved; null inside the envelope is ambiguous and
		// therefore invalid.
		return cty.NilVal, path.NewErrorf("null capsule value for %q version %d must be encoded as a bare JSON null", wire.ID, wire.Version)
	}

	entry, err := r.lookupEntry(wire.ID, wire.Version)
	if err != nil {
		return cty.NilVal, path.NewError(err)
	}
	if !entry.ty.Equals(want) {
		return cty.NilVal, path.NewErrorf(
			"capsule value descriptor %q version %d does not match the target type %s",
			wire.ID, wire.Version, want.FriendlyName(),
		)
	}

	var native any
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("decode callback for capsule type %q version %d panicked: %v", entry.id, entry.version, recovered)
				native = nil
			}
		}()
		native, err = entry.decode(wire.Value)
	}()
	if err != nil {
		return cty.NilVal, path.NewError(err)
	}

	if err := validateCapsuleNative(native, want); err != nil {
		return cty.NilVal, path.NewError(err)
	}

	// validateCapsuleNative above checks everything CapsuleVal would panic
	// on, but the panic guard is kept so that a capsule value can never
	// escape this entry point.
	var result cty.Value
	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("failed to construct capsule value of type %s: %v", want.FriendlyName(), recovered)
			}
		}()
		result = cty.CapsuleVal(entry.ty, native)
	}()
	if err != nil {
		return cty.NilVal, path.NewError(err)
	}
	return result, nil
}

// validateCapsuleNative checks that a decode callback returned a usable
// pointer to the capsule type's native value, turning every form of bad
// result into an ordinary error instead of letting cty.CapsuleVal panic.
func validateCapsuleNative(native any, t cty.Type) error {
	rv := reflect.ValueOf(native)
	if !rv.IsValid() {
		return fmt.Errorf("decode callback for capsule type %s returned nil instead of a native pointer", t.FriendlyName())
	}
	if rv.Kind() != reflect.Ptr {
		return fmt.Errorf("decode callback for capsule type %s returned %T, which is not a pointer", t.FriendlyName(), native)
	}
	if rv.IsNil() {
		return fmt.Errorf("decode callback for capsule type %s returned a nil %s pointer", t.FriendlyName(), rv.Type())
	}
	want := t.EncapsulatedType()
	if got := rv.Type().Elem(); !got.AssignableTo(want) {
		return fmt.Errorf("decode callback for capsule type %s returned %s, which is not compatible with the native type %s", t.FriendlyName(), rv.Type(), reflect.PtrTo(want))
	}
	return nil
}

// decodeStrict decodes buf into target, rejecting unknown fields and any
// data following the first JSON value.
func decodeStrict(buf []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(buf))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return errors.New("extraneous data after JSON value")
		}
		return err
	}
	return nil
}
