// Package lint implements the argus precision rules ARGUS-L001..L006: a spec that is
// imprecise about types cannot be diffed meaningfully, so imprecision is itself a finding.
package lint

import (
	"fmt"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

type Finding struct {
	Rule    string // ARGUS-L00x
	Pointer string // JSON pointer into the spec, e.g. #/components/schemas/Price/properties/rate
	Message string
}

var Rules = map[string]string{
	"ARGUS-L001": "untyped schema ({}): generator could not infer a type",
	"ARGUS-L002": "additionalProperties is an untyped schema: map values have no type",
	"ARGUS-L003": "type: number without format (float|double)",
	"ARGUS-L004": "type: integer without format (int32|int64)",
	"ARGUS-L005": "2xx response without a content schema",
	"ARGUS-L006": "OpenAPI version is not 3.0.x or 3.1.x",
}

// Lint walks every operation and every component schema once.
func Lint(doc *openapi3.T) []Finding {
	l := &linter{seen: map[*openapi3.Schema]bool{}}
	if !strings.HasPrefix(doc.OpenAPI, "3.0.") && !strings.HasPrefix(doc.OpenAPI, "3.1.") {
		l.add("ARGUS-L006", "#/openapi", "got %q", doc.OpenAPI)
	}
	if doc.Components != nil {
		for _, name := range sortedKeys(doc.Components.Schemas) {
			l.schema(doc.Components.Schemas[name], "#/components/schemas/"+esc(name))
		}
	}
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		for method, op := range item.Operations() {
			base := "#/paths/" + esc(path) + "/" + strings.ToLower(method)
			for _, p := range op.Parameters {
				if p.Value != nil {
					l.schema(p.Value.Schema, base+"/parameters/"+esc(p.Value.Name)+"/schema")
				}
			}
			if op.RequestBody != nil && op.RequestBody.Value != nil {
				for mt, c := range op.RequestBody.Value.Content {
					l.schema(c.Schema, base+"/requestBody/content/"+esc(mt)+"/schema")
				}
			}
			if op.Responses == nil {
				continue
			}
			for code, r := range op.Responses.Map() {
				ptr := base + "/responses/" + code
				if r.Value == nil {
					continue
				}
				if is2xx(code) && code != "204" && !hasSchema(r.Value) {
					l.add("ARGUS-L005", ptr, "%s %s %s", method, path, code)
				}
				for mt, c := range r.Value.Content {
					l.schema(c.Schema, ptr+"/content/"+esc(mt)+"/schema")
				}
			}
		}
	}
	slices.SortFunc(l.out, func(a, b Finding) int { return strings.Compare(a.Pointer+a.Rule, b.Pointer+b.Rule) })
	return l.out
}

type linter struct {
	seen map[*openapi3.Schema]bool
	out  []Finding
}

func (l *linter) add(rule, ptr, f string, a ...any) {
	l.out = append(l.out, Finding{Rule: rule, Pointer: ptr, Message: Rules[rule] + ": " + fmt.Sprintf(f, a...)})
}

func (l *linter) schema(ref *openapi3.SchemaRef, ptr string) {
	// $ref targets are linted once under #/components/schemas, which also breaks cycles.
	if ref == nil || ref.Value == nil || ref.Ref != "" || l.seen[ref.Value] {
		return
	}
	s := ref.Value
	l.seen[s] = true
	if untyped(s) {
		l.add("ARGUS-L001", ptr, "add a concrete type (Java: concrete DTO / @Schema(implementation=...))")
	}
	if ap := s.AdditionalProperties.Schema; ap != nil && ap.Ref == "" && ap.Value != nil && untyped(ap.Value) {
		l.add("ARGUS-L002", ptr+"/additionalProperties", "type the map values")
		l.seen[ap.Value] = true // L002 says it all; do not add L001 for the same schema
	}
	if s.Type != nil && s.Type.Includes(openapi3.TypeNumber) && s.Format != "float" && s.Format != "double" {
		l.add("ARGUS-L003", ptr, "format is %q", s.Format)
	}
	if s.Type != nil && s.Type.Includes(openapi3.TypeInteger) && s.Format != "int32" && s.Format != "int64" {
		l.add("ARGUS-L004", ptr, "format is %q", s.Format)
	}
	for _, name := range sortedKeys(s.Properties) {
		l.schema(s.Properties[name], ptr+"/properties/"+esc(name))
	}
	l.schema(s.Items, ptr+"/items")
	l.schema(s.AdditionalProperties.Schema, ptr+"/additionalProperties")
	for kw, list := range map[string]openapi3.SchemaRefs{"allOf": s.AllOf, "oneOf": s.OneOf, "anyOf": s.AnyOf} {
		for i, r := range list {
			l.schema(r, fmt.Sprintf("%s/%s/%d", ptr, kw, i))
		}
	}
}

// untyped: nothing constrains the value's shape. Composition-only schemas (allOf/oneOf/anyOf),
// enum/const and object-ish schemas with properties count as typed.
func untyped(s *openapi3.Schema) bool {
	return (s.Type == nil || len(*s.Type) == 0) &&
		len(s.Properties) == 0 && s.Items == nil && len(s.AllOf)+len(s.OneOf)+len(s.AnyOf) == 0 &&
		len(s.Enum) == 0 && s.Const == nil && s.Not == nil
}

func hasSchema(r *openapi3.Response) bool {
	for _, c := range r.Content {
		if c.Schema != nil {
			return true
		}
	}
	return false
}

func is2xx(code string) bool { return len(code) == 3 && code[0] == '2' || code == "2XX" }

func esc(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "~", "~0"), "/", "~1") }

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
