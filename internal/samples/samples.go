package samples

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/getkin/kin-openapi/openapi3"
)

// Result is one problem in one sample file.
type Result struct {
	OperationID string
	File        string // target/argus-samples/getProducts/3.json
	Pointer     string // /0/price/selling_price
	Kind        string // lexical-integer | schema
	Detail      string
}

// Report summarises a run; Uncovered lists operations without any sample.
type Report struct {
	Files     int
	Results   []Result
	Uncovered []string
}

// Run checks every <dir>/<operationId>/<n>.json against the success response schema.
func Run(doc *openapi3.T, dir string) (*Report, error) {
	ops := operations(doc)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	rep := &Report{}
	covered := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		opID := e.Name()
		op, ok := ops[opID]
		if !ok {
			// A renamed operationId would otherwise silently skip all its samples.
			return nil, fmt.Errorf("%s: no operation with operationId %q in spec", filepath.Join(dir, opID), opID)
		}
		schema := successSchema(op)
		files, _ := filepath.Glob(filepath.Join(dir, opID, "*.json"))
		for _, f := range files {
			body, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			rep.Files++
			covered[opID] = true
			if schema == nil {
				continue // ARGUS-L005 already reports the missing schema
			}
			lex, err := CheckIntegers(body, schema)
			if err != nil {
				rep.Results = append(rep.Results, Result{opID, f, "", "schema", "invalid JSON: " + err.Error()})
				continue
			}
			for _, l := range lex {
				rep.Results = append(rep.Results, Result{opID, f, l.Pointer, "lexical-integer",
					fmt.Sprintf("integer field carried %s; Go encoding/json rejects it into int", l.Token)})
			}
			var v any
			if err := json.Unmarshal(body, &v); err != nil {
				continue // already reported above
			}
			if err := doc.ValidateSchemaJSON(schema, v, openapi3.MultiErrors()); err != nil {
				forEachSchemaError(err, func(se *openapi3.SchemaError) {
					rep.Results = append(rep.Results, Result{opID, f, "/" + joinPtr(se.JSONPointer()), "schema", se.Reason})
				})
			}
		}
	}
	for id := range ops {
		if !covered[id] {
			rep.Uncovered = append(rep.Uncovered, id)
		}
	}
	slices.Sort(rep.Uncovered)
	return rep, nil
}

func operations(doc *openapi3.T) map[string]*openapi3.Operation {
	out := map[string]*openapi3.Operation{}
	for _, item := range doc.Paths.Map() {
		for _, op := range item.Operations() {
			if op.OperationID != "" {
				out[op.OperationID] = op
			}
		}
	}
	return out
}

// successSchema picks 200, then 201, then the lowest other 2xx with application/json.
// shortcut: samples carry no status code, so non-200 success bodies need their own operationId dir layout if added later.
func successSchema(op *openapi3.Operation) *openapi3.Schema {
	for _, c := range []int{200, 201, 202, 203, 206} {
		r := op.Responses.Status(c)
		if r == nil || r.Value == nil {
			continue
		}
		if mt := r.Value.Content.Get("application/json"); mt != nil && mt.Schema != nil {
			return mt.Schema.Value
		}
	}
	return nil
}

func forEachSchemaError(err error, f func(*openapi3.SchemaError)) {
	var me openapi3.MultiError
	if errors.As(err, &me) {
		for _, e := range me {
			forEachSchemaError(e, f)
		}
		return
	}
	var se *openapi3.SchemaError
	if errors.As(err, &se) {
		f(se)
	}
}

func joinPtr(p []string) string {
	out := ""
	for i, s := range p {
		if i > 0 {
			out += "/"
		}
		out += escape(s)
	}
	return out
}
