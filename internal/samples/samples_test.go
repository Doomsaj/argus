package samples

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

const spec = `{"openapi":"3.1.0","info":{"title":"t","version":"1"},"paths":{},
"components":{"schemas":{
 "Price":{"type":"object","properties":{"selling_price":{"type":"integer","format":"int64"},"rate":{"type":"number","format":"double"}}},
 "Item":{"allOf":[{"$ref":"#/components/schemas/Base"},{"type":"object","properties":{"price":{"$ref":"#/components/schemas/Price"},"limit":{"type":["integer","null"]}}}]},
 "Base":{"type":"object","properties":{"id":{"type":"integer"}}},
 "List":{"type":"array","items":{"$ref":"#/components/schemas/Item"}}}}}`

func TestCheckIntegers(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromData([]byte(spec))
	if err != nil {
		t.Fatal(err)
	}
	if err := doc.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := doc.Components.Schemas["List"].Value
	body := `[{"id":1,"price":{"selling_price":1250000.0,"rate":4.0},"limit":null},
	          {"id":2e0,"price":{"selling_price":10,"rate":4.5},"limit":3.5,"extra":{"x":1.5}}]`
	got, err := CheckIntegers([]byte(body), s)
	if err != nil {
		t.Fatal(err)
	}
	want := []Finding{{"/0/price/selling_price", "1250000.0"}, {"/1/id", "2e0"}, {"/1/limit", "3.5"}}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("finding %d: got %v want %v", i, got[i], want[i])
		}
	}
	// kin-openapi alone accepts 1250000.0 as an integer: the gap the lexical check closes.
	var v any
	_ = json.Unmarshal([]byte(`{"selling_price":1250000.0}`), &v)
	if err := doc.ValidateSchemaJSON(doc.Components.Schemas["Price"].Value, v); err != nil {
		t.Logf("kin-openapi now rejects 1250000.0 (lexical check still needed for 2e0): %v", err)
	}
}

func load(t *testing.T) *openapi3.T {
	t.Helper()
	l := openapi3.NewLoader()
	doc, err := l.LoadFromFile("../../testdata/check/base.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

// The incident body: 1250000.0 and 1e6 in integer fields must be lexical findings.
func TestRunIncident(t *testing.T) {
	rep, err := Run(load(t), "../../testdata/samples")
	if err != nil {
		t.Fatal(err)
	}
	var lex []string
	for _, r := range rep.Results {
		if r.Kind == "lexical-integer" {
			lex = append(lex, r.Pointer+"="+r.Detail)
		}
	}
	if rep.Files != 1 || len(lex) != 2 || !strings.Contains(lex[0], "/0/selling_price=integer field carried 1250000.0") ||
		!strings.Contains(lex[1], "/1/selling_price=integer field carried 1e6") {
		t.Fatalf("got %+v", rep)
	}
}

func TestRunClean(t *testing.T) {
	rep, err := Run(load(t), "../../testdata/samples-ok")
	if err != nil || rep.Files != 1 || len(rep.Results) != 0 {
		t.Fatalf("got %+v %v", rep, err)
	}
}

func TestRunSchemaViolation(t *testing.T) {
	dir := t.TempDir()
	writeSample(t, dir, "getProducts", `[{"id":"x"}]`)
	rep, err := Run(load(t), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) == 0 || rep.Results[0].Kind != "schema" {
		t.Fatalf("want schema problems, got %+v", rep)
	}
}

func TestRunUnknownOperation(t *testing.T) {
	dir := t.TempDir()
	writeSample(t, dir, "renamedOp", `[]`)
	if _, err := Run(load(t), dir); err == nil || !strings.Contains(err.Error(), "renamedOp") {
		t.Fatalf("want unknown operationId error, got %v", err)
	}
}

func writeSample(t *testing.T, dir, op, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, op), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, op, "1.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
