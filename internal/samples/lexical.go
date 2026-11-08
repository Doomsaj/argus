// Package samples checks recorded provider responses against the spec.
package samples

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Finding is one lexical violation: an integer-typed field carried a token
// with a fraction or exponent (1.0, 1.5, 1e3). Go's encoding/json rejects these
// into int even though JSON Schema (draft-06+, OAS 3.1, kin-openapi) accepts 1.0.
type Finding struct {
	Pointer string // RFC 6901 JSON pointer into the sample, e.g. /0/price/selling_price
	Token   string // raw token as it appeared on the wire
}

// CheckIntegers walks body with a token stream and the response schema side by side.
func CheckIntegers(body []byte, schema *openapi3.Schema) ([]Finding, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber() // keep the raw lexical form of every number
	w := walker{dec: dec}
	if err := w.value(schema, ""); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return w.out, nil
}

type walker struct {
	dec *json.Decoder
	out []Finding
}

// view is the merged effect of a schema and its allOf members (refs are already
// resolved by the kin-openapi loader, so SchemaRef.Value is the target).
type view struct {
	integer bool                        // some member says type: integer
	props   map[string]*openapi3.Schema // merged properties
	addl    *openapi3.Schema            // additionalProperties schema, if typed
	items   *openapi3.Schema
	alts    []*openapi3.Schema // oneOf/anyOf branches, resolved per value
}

func merge(s *openapi3.Schema, v *view, depth int) {
	if s == nil || depth > 32 { // depth guard against pathological recursive allOf
		return
	}
	if s.Type != nil && s.Type.Includes(openapi3.TypeInteger) && !s.Type.Includes(openapi3.TypeNumber) {
		v.integer = true
	}
	for name, p := range s.Properties {
		if p != nil && p.Value != nil {
			if v.props == nil {
				v.props = map[string]*openapi3.Schema{}
			}
			if _, seen := v.props[name]; !seen {
				v.props[name] = p.Value
			}
		}
	}
	if s.AdditionalProperties.Schema != nil && v.addl == nil {
		v.addl = s.AdditionalProperties.Schema.Value
	}
	if s.Items != nil && v.items == nil {
		v.items = s.Items.Value
	}
	for _, r := range s.AllOf {
		merge(r.Value, v, depth+1)
	}
	for _, r := range append(append(openapi3.SchemaRefs{}, s.OneOf...), s.AnyOf...) {
		v.alts = append(v.alts, r.Value)
	}
}

func viewOf(s *openapi3.Schema) view {
	var v view
	merge(s, &v, 0)
	// oneOf/anyOf: a number is integer-typed only if every branch that admits
	// numbers says integer (otherwise the spec itself allows a float there).
	if len(v.alts) > 0 && !v.integer {
		numeric, allInt := 0, true
		for _, a := range v.alts {
			av := viewOf(a)
			if a != nil && a.Type != nil && (a.Type.Includes(openapi3.TypeNumber) || a.Type.Includes(openapi3.TypeInteger)) {
				numeric++
				allInt = allInt && av.integer
			}
		}
		v.integer = numeric > 0 && allInt
	}
	return v
}

// value consumes exactly one JSON value from the stream.
func (w *walker) value(s *openapi3.Schema, ptr string) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	v := viewOf(s)
	switch t := tok.(type) {
	case json.Number:
		if v.integer && strings.ContainsAny(t.String(), ".eE") {
			w.out = append(w.out, Finding{Pointer: ptr, Token: t.String()})
		}
	case json.Delim:
		switch t {
		case '{':
			for w.dec.More() {
				kt, err := w.dec.Token()
				if err != nil {
					return err
				}
				key := kt.(string)
				child := v.props[key]
				if child == nil {
					child = v.addl // nil => untyped: still walked, nothing is integer
				}
				if child == nil && len(v.alts) > 0 {
					child = branchProp(v.alts, key)
				}
				if err := w.value(child, ptr+"/"+escape(key)); err != nil {
					return err
				}
			}
			_, err = w.dec.Token() // '}'
			return err
		case '[':
			item := v.items
			for i := 0; w.dec.More(); i++ {
				if err := w.value(item, ptr+"/"+strconv.Itoa(i)); err != nil {
					return err
				}
			}
			_, err = w.dec.Token() // ']'
			return err
		}
	}
	// string, bool, null: nothing lexical to check. Nullability is kin-openapi's job.
	return nil
}

// branchProp picks the first oneOf/anyOf branch that declares the key.
// shortcut: no discriminator support, upgrade if a provider uses discriminated unions.
func branchProp(alts []*openapi3.Schema, key string) *openapi3.Schema {
	for _, a := range alts {
		if p := viewOf(a).props[key]; p != nil {
			return p
		}
	}
	return nil
}

func escape(k string) string {
	return strings.ReplaceAll(strings.ReplaceAll(k, "~", "~0"), "/", "~1")
}
