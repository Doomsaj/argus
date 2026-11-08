// Package pactgen turns a Go response struct into a Pact V3/V4 JSON body whose
// matchers encode what the Go decoder actually needs:
//
//	int*, uint*   -> matchers.Integer   (provider must send an integer token; 1.0 fails, spike S2)
//	float*        -> matchers.Like(1.5) (any JSON number; not Decimal, which rejects 10)
//	string, bool  -> matchers.Like
//	slice, array  -> matchers.EachLike(elem, MinItems)
//	struct        -> nested map (embedded structs are flattened like encoding/json)
//	map[string]X  -> skipped (dynamic keys)
//	*T            -> skipped unless tagged `pact:"required"`
//	any/interface -> skipped
//
// Field names follow `json` tags; `json:"-"` and `pact:"-"` exclude a field.
// JSON paths in options use the form $[*].price.selling_price ("[*]" = every array element).
package pactgen

import (
	"encoding"
	"encoding/json"
	"reflect"
	"strings"
	"time"

	"github.com/pact-foundation/pact-go/v2/matchers"
)

// Option tunes Body.
type Option func(*builder)

// Override replaces the generated matcher at jsonPath (also includes a skipped field,
// e.g. a pointer the provider state guarantees, or a map with known keys).
func Override(jsonPath string, m matchers.Matcher) Option {
	return func(b *builder) { b.override[jsonPath] = m }
}

// Skip leaves jsonPath out of the contract (field the consumer reads but tolerates missing).
func Skip(jsonPath string) Option {
	return func(b *builder) { b.skip[jsonPath] = true }
}

// MinItems sets the minimum array length for every EachLike (default 1).
func MinItems(n int) Option {
	return func(b *builder) { b.minItems = n }
}

// Body returns the matcher tree for v (a struct, pointer to struct, or slice value).
// Use with V3/V4 builders: .WillRespondWith(200, func(b *consumer.V4ResponseBuilder) {
// b.JSONBody(pactgen.Body([]productDetailItem{})) }).
func Body(v any, opts ...Option) any {
	b := &builder{seen: map[reflect.Type]bool{}, override: map[string]matchers.Matcher{}, skip: map[string]bool{}, minItems: 1}
	for _, o := range opts {
		o(b)
	}
	return b.build(reflect.TypeOf(v), "$", true)
}

var (
	timeType      = reflect.TypeFor[time.Time]()
	jsonMarshaler = reflect.TypeFor[json.Marshaler]()
	textMarshaler = reflect.TypeFor[encoding.TextMarshaler]()
)

type builder struct {
	seen     map[reflect.Type]bool // recursion guard for self-referencing types
	override map[string]matchers.Matcher
	skip     map[string]bool
	minItems int
}

// build returns nil when the path is left out of the contract.
func (b *builder) build(t reflect.Type, path string, include bool) any {
	if m, ok := b.override[path]; ok {
		return m
	}
	if b.skip[path] || !include || t == nil {
		return nil
	}
	switch {
	case t == timeType:
		return matchers.Like("2026-01-01T00:00:00Z")
	case t.Kind() != reflect.Pointer && (t.Implements(jsonMarshaler) || reflect.PointerTo(t).Implements(jsonMarshaler)):
		return nil // custom MarshalJSON/UnmarshalJSON: wire shape unknown, use Override
	case t.Kind() == reflect.String && (t.Implements(textMarshaler) || reflect.PointerTo(t).Implements(textMarshaler)):
		return matchers.Like("x")
	}
	switch t.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return matchers.Integer(1)
	case reflect.Float32, reflect.Float64:
		return matchers.Like(1.5)
	case reflect.String:
		return matchers.Like("x")
	case reflect.Bool:
		return matchers.Like(true)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return matchers.Like("eA==") // []byte is base64 on the wire
		}
		elem := b.build(t.Elem(), path+"[*]", true)
		if elem == nil {
			return nil
		}
		return matchers.EachLike(elem, b.minItems)
	case reflect.Pointer:
		return b.build(t.Elem(), path, true) // top-level or `pact:"required"` pointers only
	case reflect.Struct:
		if b.seen[t] {
			return nil
		}
		b.seen[t] = true
		defer delete(b.seen, t)
		m := map[string]any{}
		b.fields(t, path, m)
		return m
	default: // map (dynamic keys), interface, chan, func, complex
		return nil
	}
}

func (b *builder) fields(t reflect.Type, path string, m map[string]any) {
	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		pact := f.Tag.Get("pact")
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if pact == "-" || name == "-" {
			continue
		}
		ft := f.Type
		if f.Anonymous && name == "" { // embedded: flatten like encoding/json
			if ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				b.fields(ft, path, m)
				continue
			}
		}
		if name == "" {
			name = f.Name
		}
		// Pointers are nullable: Pact has no "number or null" matcher, so they stay out
		// unless the provider state guarantees a value.
		include := ft.Kind() != reflect.Pointer || pact == "required"
		if v := b.build(ft, path+"."+name, include); v != nil {
			m[name] = v
		}
	}
}
