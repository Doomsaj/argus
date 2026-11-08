package pactbroker

import (
	"slices"
	"testing"
)

func TestAffected(t *testing.T) {
	cs := []Consumer{
		{Name: "marcopolo", Endpoints: []string{"GET /v1/products/42?x=1", "POST /v1/products/"}},
		{Name: "other", Endpoints: []string{"GET /v1/other"}},
	}
	if got := Affected(cs, "POST", "/v1/products/"); !slices.Equal(got, []string{"marcopolo"}) {
		t.Errorf("POST: %v", got)
	}
	if got := Affected(cs, "GET", "/v1/products/{id}"); !slices.Equal(got, []string{"marcopolo"}) {
		t.Errorf("GET template: %v", got)
	}
	if got := Affected(cs, "DELETE", "/v1/products/{id}"); got != nil {
		t.Errorf("DELETE: %v", got)
	}
}
