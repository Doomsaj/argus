package lint

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
)

func TestLint(t *testing.T) {
	doc, err := openapi3.NewLoader().LoadFromFile("../../testdata/check/base.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if fs := Lint(doc); len(fs) != 0 {
		t.Fatalf("base fixture must be clean, got %+v", fs)
	}
	bad := `{"openapi":"3.0.3","info":{"title":"t","version":"1"},
"paths":{"/x":{"get":{"responses":{"200":{"description":"no content"}}}}},
"components":{"schemas":{"Item":{"type":"object","properties":{
  "rate":{"type":"number"},"id":{"type":"integer"},"any":{},
  "m":{"type":"object","additionalProperties":{}}}}}}}`
	doc, err = openapi3.NewLoader().LoadFromData([]byte(bad))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range Lint(doc) {
		got = append(got, f.Rule+" "+f.Pointer)
	}
	want := []string{
		"ARGUS-L001 #/components/schemas/Item/properties/any",
		"ARGUS-L004 #/components/schemas/Item/properties/id",
		"ARGUS-L002 #/components/schemas/Item/properties/m/additionalProperties",
		"ARGUS-L003 #/components/schemas/Item/properties/rate",
		"ARGUS-L005 #/paths/~1x/get/responses/200",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
