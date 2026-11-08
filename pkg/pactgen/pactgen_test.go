package pactgen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/pact-foundation/pact-go/v2/matchers"
)

type promotion struct {
	Discount int `json:"discount"`
}

type base struct {
	ID int64 `json:"id"`
}

// Shaped like marcopolo's productDetailPriceEntry (modules/application/internal/product/repo-v2.go).
type priceEntry struct {
	base
	SellingPrice int            `json:"selling_price"`
	Rate         float64        `json:"rate"`
	Title        string         `json:"title"`
	Active       bool           `json:"active"`
	Limit        *int           `json:"limit"`
	MaxCoverage  *int           `json:"max_coverage" pact:"required"`
	Promotion    *promotion     `json:"promotion"`
	Payload      map[string]any `json:"payload"`
	Raw          any            `json:"raw"`
	Internal     string         `json:"-"`
	Debug        string         `json:"debug" pact:"-"`
	Tags         []string       `json:"tags"`
}

func render(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBody(t *testing.T) {
	got := render(t, Body([]priceEntry{}))
	for _, want := range []string{
		`"selling_price":{"specification":"3.0.0","pact:matcher:type":"integer","value":1}`,
		`"id":{"specification":"3.0.0","pact:matcher:type":"integer","value":1}`,
		`"rate":{"specification":"2.0.0","pact:matcher:type":"type","value":1.5}`,
		`"max_coverage":{"specification":"3.0.0","pact:matcher:type":"integer"`,
		`"tags":{`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, got)
		}
	}
	for _, absent := range []string{`"limit"`, `"promotion"`, `"payload"`, `"raw"`, `"Internal"`, `"debug"`, `"decimal"`} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %s in\n%s", absent, got)
		}
	}
}

func TestOptions(t *testing.T) {
	got := render(t, Body([]priceEntry{},
		Override("$[*].promotion", matchers.Like(map[string]any{"discount": matchers.Integer(10)})),
		Skip("$[*].title"),
		MinItems(2),
	))
	if !strings.Contains(got, `"promotion"`) || strings.Contains(got, `"title"`) || !strings.Contains(got, `"min":2`) {
		t.Errorf("options not applied:\n%s", got)
	}
}
