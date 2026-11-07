package stdlib

import (
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

// ssObj builds an object value from the given attribute values.
func ssObj(vals map[string]cty.Value) cty.Value {
	return cty.ObjectVal(vals)
}

// ssKey builds one sort-key specification object.
func ssKey(path []string, order, nulls string) cty.Value {
	if len(path) == 0 {
		return cty.ObjectVal(map[string]cty.Value{
			"path":  cty.ListValEmpty(cty.String),
			"order": cty.StringVal(order),
			"nulls": cty.StringVal(nulls),
		})
	}
	steps := make([]cty.Value, len(path))
	for i, step := range path {
		steps[i] = cty.StringVal(step)
	}
	return cty.ObjectVal(map[string]cty.Value{
		"path":  cty.ListVal(steps),
		"order": cty.StringVal(order),
		"nulls": cty.StringVal(nulls),
	})
}

// ssKeys builds the sort-keys list argument.
func ssKeys(keys ...cty.Value) cty.Value {
	return cty.ListVal(keys)
}

// ssReorder returns a new slice containing the given records in the given
// original-index order.
func ssReorder(records []cty.Value, indices ...int) []cty.Value {
	out := make([]cty.Value, len(indices))
	for i, idx := range indices {
		out[i] = records[idx]
	}
	return out
}

func TestStableSortBy(t *testing.T) {
	num := cty.Number
	str := cty.String

	tests := []struct {
		name string
		list cty.Value
		keys cty.Value
		want cty.Value
	}{
		{
			name: "numbers ascending",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(3), "t": cty.StringVal("c")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "t": cty.StringVal("b")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "t": cty.StringVal("b")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(3), "t": cty.StringVal("c")}),
			}),
		},
		{
			name: "numbers descending",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(3), "t": cty.StringVal("c")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "t": cty.StringVal("b")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "desc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(3), "t": cty.StringVal("c")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "t": cty.StringVal("b")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
			}),
		},
		{
			name: "equal primary key keeps original order (stable)",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("first")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("second")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("third")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("first")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("second")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "t": cty.StringVal("third")}),
			}),
		},
		{
			name: "multiple keys with tie on primary",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(2)}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(2), "s": cty.NumberIntVal(0)}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1)}),
			}),
			keys: ssKeys(
				ssKey([]string{"g"}, "asc", "last"),
				ssKey([]string{"s"}, "desc", "last"),
			),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(2)}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1)}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(2), "s": cty.NumberIntVal(0)}),
			}),
		},
		{
			name: "all keys equal preserves original positions",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("b")}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("c")}),
			}),
			keys: ssKeys(
				ssKey([]string{"g"}, "asc", "last"),
				ssKey([]string{"s"}, "asc", "last"),
			),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("b")}),
				ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1), "t": cty.StringVal("c")}),
			}),
		},
		{
			name: "strings use normalized byte order",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"name": cty.StringVal("banana")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("é")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("Cherry")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("apple")}),
			}),
			keys: ssKeys(ssKey([]string{"name"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"name": cty.StringVal("Cherry")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("apple")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("banana")}),
				ssObj(map[string]cty.Value{"name": cty.StringVal("é")}),
			}),
		},
		{
			name: "booleans false first, descending reverses values only",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"b": cty.True, "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"b": cty.False, "t": cty.StringVal("b")}),
				ssObj(map[string]cty.Value{"b": cty.True, "t": cty.StringVal("c")}),
			}),
			keys: ssKeys(ssKey([]string{"b"}, "desc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"b": cty.True, "t": cty.StringVal("a")}),
				ssObj(map[string]cty.Value{"b": cty.True, "t": cty.StringVal("c")}),
				ssObj(map[string]cty.Value{"b": cty.False, "t": cty.StringVal("b")}),
			}),
		},
		{
			name: "map keys can be traversed",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"m": cty.MapVal(map[string]cty.Value{"city": cty.StringVal("Berlin")})}),
				ssObj(map[string]cty.Value{"m": cty.MapVal(map[string]cty.Value{"city": cty.StringVal("Amsterdam")})}),
			}),
			keys: ssKeys(ssKey([]string{"m", "city"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"m": cty.MapVal(map[string]cty.Value{"city": cty.StringVal("Amsterdam")})}),
				ssObj(map[string]cty.Value{"m": cty.MapVal(map[string]cty.Value{"city": cty.StringVal("Berlin")})}),
			}),
		},
		{
			name: "nested object attributes and map keys together",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{
					"addr": ssObj(map[string]cty.Value{
						"city": cty.MapVal(map[string]cty.Value{"name": cty.StringVal("Zürich")}),
					}),
				}),
				ssObj(map[string]cty.Value{
					"addr": ssObj(map[string]cty.Value{
						"city": cty.MapVal(map[string]cty.Value{"name": cty.StringVal("Aarau")}),
					}),
				}),
			}),
			keys: ssKeys(ssKey([]string{"addr", "city", "name"}, "asc", "last")),
			want: cty.NilVal, // replaced below
		},
		{
			name: "null attribute treated as null key, nulls last ascending",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"v": cty.NullVal(num)}),
				ssObj(map[string]cty.Value{"v": cty.NumberIntVal(1)}),
			}),
			keys: ssKeys(ssKey([]string{"v"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"v": cty.NumberIntVal(1)}),
				ssObj(map[string]cty.Value{"v": cty.NullVal(num)}),
			}),
		},
		{
			name: "null intermediate object resolves to null key",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"o": cty.NullVal(cty.Object(map[string]cty.Type{"x": str}))}),
				ssObj(map[string]cty.Value{"o": ssObj(map[string]cty.Value{"x": cty.StringVal("a")})}),
			}),
			keys: ssKeys(ssKey([]string{"o", "x"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"o": ssObj(map[string]cty.Value{"x": cty.StringVal("a")})}),
				ssObj(map[string]cty.Value{"o": cty.NullVal(cty.Object(map[string]cty.Type{"x": str}))}),
			}),
		},
		{
			name: "unrelated unknown payload does not block known order",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "note": cty.UnknownVal(str)}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "note": cty.StringVal("known")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(1), "note": cty.StringVal("known")}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(2), "note": cty.UnknownVal(str)}),
			}),
		},
		{
			name: "different primary keys: unknown secondary ignored",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"a": cty.NumberIntVal(2), "b": cty.UnknownVal(num)}),
				ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.UnknownVal(num)}),
			}),
			keys: ssKeys(
				ssKey([]string{"a"}, "asc", "last"),
				ssKey([]string{"b"}, "asc", "last"),
			),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.UnknownVal(num)}),
				ssObj(map[string]cty.Value{"a": cty.NumberIntVal(2), "b": cty.UnknownVal(num)}),
			}),
		},
		{
			name: "infinities order with arbitrary precision numbers",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.PositiveInfinity}),
				ssObj(map[string]cty.Value{"n": cty.NegativeInfinity}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(5)}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.NegativeInfinity}),
				ssObj(map[string]cty.Value{"n": cty.NumberIntVal(5)}),
				ssObj(map[string]cty.Value{"n": cty.PositiveInfinity}),
			}),
		},
		{
			name: "integers beyond float64 precision keep distinct order",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9007199254740993")}), // 2^53 + 1
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9007199254740992")}), // 2^53
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("1")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("1")}),
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9007199254740992")}),
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9007199254740993")}),
			}),
		},
		{
			name: "very large integers compare by exact value",
			list: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("10000000000000000000000000000000000000000")}),
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9999999999999999999999999999999999999999")}),
			}),
			keys: ssKeys(ssKey([]string{"n"}, "asc", "last")),
			want: cty.ListVal([]cty.Value{
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("9999999999999999999999999999999999999999")}),
				ssObj(map[string]cty.Value{"n": cty.MustParseNumberVal("10000000000000000000000000000000000000000")}),
			}),
		},
	}

	// Fix up the one nested case that is easier to express via reordering.
	for i := range tests {
		if tests[i].name == "nested object attributes and map keys together" {
			records := tests[i].list.AsValueSlice()
			tests[i].want = cty.ListVal(ssReorder(records, 1, 0))
		}
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := StableSortBy(test.list, test.keys)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			if !got.RawEquals(test.want) {
				t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", got, test.want)
			}
		})
	}
}

func TestStableSortByNullPlacement(t *testing.T) {
	num := cty.Number
	mk := func(n int64) cty.Value {
		return ssObj(map[string]cty.Value{"v": cty.NumberIntVal(n)})
	}
	nullRecord := ssObj(map[string]cty.Value{"v": cty.NullVal(num)})
	// Input order is intentionally 2, null, 1.
	input := cty.ListVal([]cty.Value{mk(2), nullRecord, mk(1)})

	tests := []struct {
		order, nulls string
		wantIndices  []int
	}{
		{"asc", "first", []int{1, 2, 0}},  // null, 1, 2
		{"asc", "last", []int{2, 0, 1}},   // 1, 2, null
		{"desc", "first", []int{1, 0, 2}}, // null, 2, 1
		{"desc", "last", []int{0, 2, 1}},  // 2, 1, null
	}
	for _, test := range tests {
		t.Run(test.order+"-"+test.nulls, func(t *testing.T) {
			keys := ssKeys(ssKey([]string{"v"}, test.order, test.nulls))
			got, err := StableSortBy(input, keys)
			if err != nil {
				t.Fatalf("unexpected error: %s", err)
			}
			want := cty.ListVal(ssReorder(input.AsValueSlice(), test.wantIndices...))
			if !got.RawEquals(want) {
				t.Fatalf("wrong result for %s/%s\ngot:  %#v\nwant: %#v", test.order, test.nulls, got, want)
			}
		})
	}
}

func TestStableSortByNullSecondaryKey(t *testing.T) {
	// Nulls on a secondary key only matter for records tied on the primary.
	num := cty.Number
	r0 := ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NumberIntVal(1)})
	r1 := ssObj(map[string]cty.Value{"g": cty.NumberIntVal(1), "s": cty.NullVal(num)})
	r2 := ssObj(map[string]cty.Value{"g": cty.NumberIntVal(2), "s": cty.NullVal(num)})
	input := cty.ListVal([]cty.Value{r0, r1, r2})
	keys := ssKeys(
		ssKey([]string{"g"}, "asc", "last"),
		ssKey([]string{"s"}, "asc", "last"),
	)
	// g=1 group: 1 before null; the g=2 record follows the whole group.
	want := cty.ListVal([]cty.Value{r0, r1, r2})
	got, err := StableSortBy(input, keys)
	if err != nil {
		t.Fatalf("unexpected error: %s", err)
	}
	if !got.RawEquals(want) {
		t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestStableSortByUnknownDecision(t *testing.T) {
	num := cty.Number
	elemTy := cty.Object(map[string]cty.Type{"a": num, "b": num})

	// unknownListOfExactLength is the idiomatic cty representation of an
	// undecidable result: the container has an exactly known length, but
	// every position is an unknown element rather than a guessed ordering.
	unknownListOfExactLength := func(n int) cty.Value {
		elems := make([]cty.Value, n)
		for i := range elems {
			elems[i] = cty.UnknownVal(elemTy)
		}
		return cty.ListVal(elems)
	}

	t.Run("unknown key among tied records yields unknown elements of exact length", func(t *testing.T) {
		input := cty.ListVal([]cty.Value{
			ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.UnknownVal(num)}),
			ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.NumberIntVal(5)}),
		})
		keys := ssKeys(
			ssKey([]string{"a"}, "asc", "last"),
			ssKey([]string{"b"}, "asc", "last"),
		)
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := unknownListOfExactLength(2)
		if !got.RawEquals(want) {
			t.Fatalf("wrong unknown result\ngot:  %#v\nwant: %#v", got, want)
		}
		if got.LengthInt() != 2 {
			t.Fatalf("exact length constraint lost: %#v", got)
		}
	})

	t.Run("unknown is not pushed to the end", func(t *testing.T) {
		// The unknown record sorts within the a=1 group in the middle; since
		// its position is undecidable no position is revealed, rather than
		// moving the unknown record to the end.
		input := cty.ListVal([]cty.Value{
			ssObj(map[string]cty.Value{"a": cty.NumberIntVal(2), "b": cty.NumberIntVal(0)}),
			ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.UnknownVal(num)}),
			ssObj(map[string]cty.Value{"a": cty.NumberIntVal(1), "b": cty.NumberIntVal(5)}),
		})
		keys := ssKeys(
			ssKey([]string{"a"}, "asc", "last"),
			ssKey([]string{"b"}, "asc", "last"),
		)
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := unknownListOfExactLength(3)
		if !got.RawEquals(want) {
			t.Fatalf("wrong unknown result\ngot:  %#v\nwant: %#v", got, want)
		}
		for _, elem := range got.AsValueSlice() {
			if elem.IsKnown() {
				t.Fatalf("undecidable position was revealed as a known element: %#v", got)
			}
		}
	})

	t.Run("unknown primary key yields unknown elements of exact length", func(t *testing.T) {
		input := cty.ListVal([]cty.Value{
			ssObj(map[string]cty.Value{"a": cty.UnknownVal(num), "b": cty.NumberIntVal(1)}),
			ssObj(map[string]cty.Value{"a": cty.UnknownVal(num), "b": cty.NumberIntVal(2)}),
		})
		keys := ssKeys(ssKey([]string{"a"}, "asc", "last"))
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := unknownListOfExactLength(2)
		if !got.RawEquals(want) {
			t.Fatalf("wrong unknown result\ngot:  %#v\nwant: %#v", got, want)
		}
	})

	t.Run("unknown whole list returns unknown list", func(t *testing.T) {
		input := cty.UnknownVal(cty.List(elemTy))
		keys := ssKeys(ssKey([]string{"a"}, "asc", "last"))
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() {
			t.Fatalf("expected unknown result, got %#v", got)
		}
		if !got.Type().Equals(cty.List(elemTy)) {
			t.Fatalf("wrong type %#v", got.Type())
		}
	})
}

func TestStableSortByMissingMapKey(t *testing.T) {
	str := cty.String
	input := cty.ListVal([]cty.Value{
		ssObj(map[string]cty.Value{
			"m": cty.MapVal(map[string]cty.Value{"x": cty.StringVal("present")}),
		}),
		ssObj(map[string]cty.Value{
			"m": cty.MapValEmpty(str),
		}),
	})
	keys := ssKeys(ssKey([]string{"m", "x"}, "asc", "last"))
	_, err := StableSortBy(input, keys)
	if err == nil {
		t.Fatal("expected error for missing map key, got success")
	}
	msg := err.Error()
	for _, want := range []string{"record 1", `"x"`, "m"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error %q does not contain %q", msg, want)
		}
	}
}

type ssListMark struct{}
type ssKeyMark struct{}
type ssPayloadMark struct{}

func TestStableSortByMarks(t *testing.T) {
	t.Run("list, key, and payload marks land in the right places", func(t *testing.T) {
		r0 := ssObj(map[string]cty.Value{
			"id":   cty.NumberIntVal(2),
			"note": cty.StringVal("b"),
		})
		r1 := ssObj(map[string]cty.Value{
			"id":   cty.NumberIntVal(1).Mark(ssKeyMark{}),
			"note": cty.StringVal("a").Mark(ssPayloadMark{}),
		})
		input := cty.ListVal([]cty.Value{r0, r1}).Mark(ssListMark{})
		keys := ssKeys(ssKey([]string{"id"}, "asc", "last"))

		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}

		unmarked, topMarks := got.Unmark()
		if !topMarks.Has(ssListMark{}) {
			t.Errorf("top-level list mark not propagated to output container")
		}
		if !topMarks.Has(ssKeyMark{}) {
			t.Errorf("deciding key mark not propagated to output container")
		}
		if topMarks.Has(ssPayloadMark{}) {
			t.Errorf("payload-only mark must not be lifted to the output container")
		}

		records := unmarked.AsValueSlice()
		if len(records) != 2 {
			t.Fatalf("wrong length %d", len(records))
		}
		// r1 (id 1) sorts first and travels with its payload marks intact.
		if !records[0].GetAttr("note").HasMark(ssPayloadMark{}) {
			t.Errorf("payload mark did not travel with the moved record")
		}
		if !records[0].GetAttr("id").HasMark(ssKeyMark{}) {
			t.Errorf("key mark was stripped from inside its record")
		}
		if records[1].GetAttr("note").HasMark(ssPayloadMark{}) {
			t.Errorf("payload mark leaked onto the wrong record")
		}
	})

	t.Run("payload marks are never aggregated onto the container", func(t *testing.T) {
		r0 := ssObj(map[string]cty.Value{
			"id":   cty.NumberIntVal(2),
			"note": cty.StringVal("b").Mark(ssPayloadMark{}),
		})
		r1 := ssObj(map[string]cty.Value{
			"id":   cty.NumberIntVal(1),
			"note": cty.StringVal("a"),
		})
		input := cty.ListVal([]cty.Value{r0, r1})
		keys := ssKeys(ssKey([]string{"id"}, "asc", "last"))

		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		unmarked, topMarks := got.Unmark()
		if topMarks.Has(ssPayloadMark{}) {
			t.Fatalf("payload mark was aggregated to the container")
		}
		records := unmarked.AsValueSlice()
		// id 1 first; the marked record (id 2) is second, still marked.
		if !records[1].GetAttr("note").HasMark(ssPayloadMark{}) {
			t.Fatalf("payload mark was stripped from its record")
		}
	})

	t.Run("single element keeps top mark without comparisons", func(t *testing.T) {
		input := cty.ListVal([]cty.Value{
			ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1), "note": cty.StringVal("a")}),
		}).Mark(ssListMark{})
		keys := ssKeys(ssKey([]string{"id"}, "asc", "last"))
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		_, topMarks := got.Unmark()
		if !topMarks.Has(ssListMark{}) {
			t.Fatalf("top-level mark lost for single-element list")
		}
	})
}

func TestStableSortByEmptyAndSingle(t *testing.T) {
	num := cty.Number
	elemTy := cty.Object(map[string]cty.Type{"id": num})

	t.Run("empty list preserves marks and type", func(t *testing.T) {
		input := cty.ListValEmpty(elemTy).Mark(ssListMark{})
		keys := ssKeys(ssKey([]string{"id"}, "asc", "last"))
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		unmarked, topMarks := got.Unmark()
		if !topMarks.Has(ssListMark{}) {
			t.Fatalf("top-level mark lost for empty list")
		}
		if unmarked.LengthInt() != 0 || !unmarked.Type().Equals(cty.List(elemTy)) {
			t.Fatalf("wrong value: %#v", unmarked)
		}
	})

	t.Run("empty list still validates the configuration", func(t *testing.T) {
		input := cty.ListValEmpty(elemTy)
		keys := ssKeys(ssKey([]string{"nope"}, "asc", "last"))
		_, err := StableSortBy(input, keys)
		if err == nil || !strings.Contains(err.Error(), "no attribute") {
			t.Fatalf("expected attribute error for empty list, got %v", err)
		}
	})

	t.Run("single element still validates the configuration", func(t *testing.T) {
		input := cty.ListVal([]cty.Value{
			ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
		})
		keys := ssKeys(ssKey([]string{"nope"}, "asc", "last"))
		_, err := StableSortBy(input, keys)
		if err == nil || !strings.Contains(err.Error(), "no attribute") {
			t.Fatalf("expected attribute error for single-element list, got %v", err)
		}
	})

	t.Run("single element returns unchanged", func(t *testing.T) {
		only := ssObj(map[string]cty.Value{"id": cty.NumberIntVal(42), "note": cty.StringVal("x")})
		input := cty.ListVal([]cty.Value{only})
		keys := ssKeys(ssKey([]string{"id"}, "desc", "first"))
		got, err := StableSortBy(input, keys)
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		want := cty.ListVal([]cty.Value{only})
		if !got.RawEquals(want) {
			t.Fatalf("wrong result\ngot:  %#v\nwant: %#v", got, want)
		}
	})
}

func TestStableSortByInvalidInput(t *testing.T) {
	keys := ssKeys(ssKey([]string{"id"}, "asc", "last"))

	t.Run("tuple rejected", func(t *testing.T) {
		input := cty.TupleVal([]cty.Value{
			ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
		})
		_, err := StableSortBy(input, keys)
		if err == nil || !strings.Contains(err.Error(), "tuples are not supported") {
			t.Fatalf("expected tuple error, got %v", err)
		}
	})

	t.Run("set rejected", func(t *testing.T) {
		input := cty.SetVal([]cty.Value{
			ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
		})
		_, err := StableSortBy(input, keys)
		if err == nil || !strings.Contains(err.Error(), "sets are not supported") {
			t.Fatalf("expected set error, got %v", err)
		}
	})

	t.Run("list of non-objects rejected", func(t *testing.T) {
		input := cty.ListVal([]cty.Value{cty.NumberIntVal(1), cty.NumberIntVal(2)})
		_, err := StableSortBy(input, keys)
		if err == nil || !strings.Contains(err.Error(), "list of objects") {
			t.Fatalf("expected list-of-objects error, got %v", err)
		}
	})

	t.Run("null list rejected", func(t *testing.T) {
		input := cty.NullVal(cty.List(cty.Object(map[string]cty.Type{"id": cty.Number})))
		_, err := StableSortBy(input, keys)
		if err == nil {
			t.Fatal("expected error for null list")
		}
	})
}

func TestStableSortByInvalidConfig(t *testing.T) {
	num := cty.Number
	str := cty.String
	input := cty.ListVal([]cty.Value{
		ssObj(map[string]cty.Value{
			"id":  cty.NumberIntVal(2),
			"ids": cty.ListVal([]cty.Value{cty.NumberIntVal(1)}),
			"s":   cty.SetVal([]cty.Value{cty.StringVal("x")}),
			"t":   cty.TupleVal([]cty.Value{cty.StringVal("x")}),
		}),
		ssObj(map[string]cty.Value{
			"id":  cty.NumberIntVal(1),
			"ids": cty.ListValEmpty(num),
			"s":   cty.SetValEmpty(str),
			"t":   cty.TupleVal([]cty.Value{cty.StringVal("y")}),
		}),
	})

	tests := []struct {
		name string
		keys cty.Value
		want string
	}{
		{
			name: "no sort keys",
			keys: cty.ListValEmpty(sortKeySpecType),
			want: "at least one sort key",
		},
		{
			name: "missing attribute",
			keys: ssKeys(ssKey([]string{"nope"}, "asc", "last")),
			want: "no attribute",
		},
		{
			name: "final key is a list",
			keys: ssKeys(ssKey([]string{"ids"}, "asc", "last")),
			want: "string, number, or boolean",
		},
		{
			name: "set in path",
			keys: ssKeys(ssKey([]string{"s", "x"}, "asc", "last")),
			want: "sets are not supported",
		},
		{
			name: "tuple in path",
			keys: ssKeys(ssKey([]string{"t", "x"}, "asc", "last")),
			want: "lists and tuples are not supported",
		},
		{
			name: "empty path",
			keys: ssKeys(ssKey([]string{}, "asc", "last")),
			want: "empty path",
		},
		{
			name: "invalid order",
			keys: ssKeys(ssKey([]string{"id"}, "up", "last")),
			want: "invalid order",
		},
		{
			name: "invalid nulls",
			keys: ssKeys(ssKey([]string{"id"}, "asc", "middle")),
			want: "invalid nulls",
		},
		{
			name: "null key specification",
			keys: cty.ListVal([]cty.Value{cty.NullVal(sortKeySpecType)}),
			want: "must not be null",
		},
		{
			name: "null order",
			keys: cty.ListVal([]cty.Value{cty.ObjectVal(map[string]cty.Value{
				"path":  cty.ListVal([]cty.Value{cty.StringVal("id")}),
				"order": cty.NullVal(str),
				"nulls": cty.StringVal("last"),
			})}),
			want: "null order",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := StableSortBy(input, test.keys)
			if err == nil {
				t.Fatalf("expected error containing %q, got success", test.want)
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), test.want)
			}
		})
	}
}

func TestStableSortByUnknownConfig(t *testing.T) {
	num := cty.Number
	elemTy := cty.Object(map[string]cty.Type{"id": num})
	input := cty.ListVal([]cty.Value{
		ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1)}),
		ssObj(map[string]cty.Value{"id": cty.NumberIntVal(2)}),
	})

	t.Run("wholly unknown sort keys", func(t *testing.T) {
		got, err := StableSortBy(input, cty.UnknownVal(cty.List(sortKeySpecType)))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() || !got.Type().Equals(cty.List(elemTy)) {
			t.Fatalf("wrong result: %#v", got)
		}
	})

	t.Run("partially unknown order attribute", func(t *testing.T) {
		spec := cty.ObjectVal(map[string]cty.Value{
			"path":  cty.ListVal([]cty.Value{cty.StringVal("id")}),
			"order": cty.UnknownVal(cty.String),
			"nulls": cty.StringVal("last"),
		})
		got, err := StableSortBy(input, cty.ListVal([]cty.Value{spec}))
		if err != nil {
			t.Fatalf("unexpected error: %s", err)
		}
		if got.IsKnown() {
			t.Fatalf("expected unknown result, got %#v", got)
		}
	})
}

func TestStableSortByDoesNotMutateInputs(t *testing.T) {
	input := cty.ListVal([]cty.Value{
		ssObj(map[string]cty.Value{"id": cty.NumberIntVal(2).Mark(ssKeyMark{}), "note": cty.StringVal("b")}),
		ssObj(map[string]cty.Value{"id": cty.NumberIntVal(1), "note": cty.StringVal("a").Mark(ssPayloadMark{})}),
	}).Mark(ssListMark{})
	keys := ssKeys(
		ssKey([]string{"id"}, "desc", "first"),
		ssKey([]string{"note"}, "asc", "last"),
	)

	inputBefore := input
	keysBefore := keys

	if _, err := StableSortBy(input, keys); err != nil {
		t.Fatalf("unexpected error: %s", err)
	}

	if !input.RawEquals(inputBefore) {
		t.Fatalf("input list was modified\nbefore: %#v\nafter:  %#v", inputBefore, input)
	}
	if !keys.RawEquals(keysBefore) {
		t.Fatalf("sort key configuration was modified\nbefore: %#v\nafter:  %#v", keysBefore, keys)
	}
}
