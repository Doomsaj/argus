package diff

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oasdiff/oasdiff/checker"
	"gopkg.in/yaml.v3"

	"git.digikala.com/argus/argus/internal/config"
)

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// run applies one textual edit to the base fixture: each case is one breaking change.
func run(t *testing.T, base []byte, old, new string) []Finding {
	t.Helper()
	rev := strings.Replace(string(base), old, new, 1)
	if rev == string(base) {
		t.Fatalf("fixture edit %q did not apply", old)
	}
	fs, err := Breaking(base, []byte(rev))
	if err != nil {
		t.Fatal(err)
	}
	return fs
}

func baseSpec(t *testing.T) []byte { return mustRead(t, "../../testdata/check/base.yaml") }

func has(fs []Finding, rule, level, loc string) bool {
	for _, f := range fs {
		if f.Rule == rule && f.Level == level && f.Location == loc {
			return true
		}
	}
	return false
}

func TestGolden(t *testing.T) {
	const intProp = "selling_price: {type: integer, format: int64}"
	cases := []struct {
		name, old, new, rule, level, loc string
	}{
		// The 2026-10-28 incident: response integer -> number must be ERR.
		{"int->number", intProp, "selling_price: {type: number, format: double}",
			"response-property-type-generalized", "ERR", "POST /v1/products/ 200 /items/selling_price"},
		{"required field removed", "        " + intProp + "\n", "",
			"response-required-property-removed", "ERR", "POST /v1/products/ 200 /items/selling_price"},
		{"required->optional", "required: [id, selling_price]", "required: [id]",
			"response-property-became-optional", "ERR", "POST /v1/products/ 200 /items/selling_price"},
		{"optional removed (argus override)", "        title: {type: string}\n", "",
			"response-optional-property-removed", "ERR", "POST /v1/products/ 200 /items/title"},
		// docs/06 section 9.3: with format, request widening is request-property-type-changed ERR.
		{"request widening with format", "limit: {type: integer, format: int64}", "limit: {type: number, format: double}",
			"request-property-type-changed", "ERR", "POST /v1/products/ request /limit"},
		{"non-breaking: property added", "        title: {type: string}\n", "        title: {type: string}\n        tmp: {type: string}\n",
			"", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := run(t, baseSpec(t), c.old, c.new)
			if c.rule == "" {
				for _, f := range fs {
					if f.Level != "INFO" {
						t.Errorf("unexpected %s %s %s", f.Level, f.Rule, f.Location)
					}
				}
				return
			}
			if !has(fs, c.rule, c.level, c.loc) {
				t.Errorf("want %s %s %s, got %+v", c.level, c.rule, c.loc, fs)
			}
		})
	}
}

// Request widening without format (integer -> number) is safe for a Go consumer: never ERR.
func TestRequestWideningWithoutFormatNotERR(t *testing.T) {
	base := []byte(strings.Replace(string(baseSpec(t)), "limit: {type: integer, format: int64}", "limit: {type: integer}", 1))
	fs := run(t, base, "limit: {type: integer}", "limit: {type: number}")
	for _, f := range fs {
		if f.Level == "ERR" {
			t.Errorf("unexpected ERR %s %s", f.Rule, f.Location)
		}
	}
	if !has(fs, "request-property-type-generalized", "INFO", "POST /v1/products/ request /limit") {
		t.Errorf("want INFO request-property-type-generalized, got %+v", fs)
	}
}

func TestEnumValueAdded(t *testing.T) {
	base := []byte(strings.Replace(string(baseSpec(t)), "        title: {type: string}\n", "        title: {type: string}\n        state: {type: string, enum: [a, b]}\n", 1))
	fs := run(t, base, "enum: [a, b]", "enum: [a, b, c]")
	if !has(fs, "response-property-enum-value-added", "ERR", "POST /v1/products/ 200 /items/state") {
		t.Fatalf("got %+v", fs)
	}
}

func TestWaiverLifecycle(t *testing.T) {
	loc := "POST /v1/products/ 200 /items/selling_price"
	w := config.Waiver{Rule: "response-property-type-generalized", Path: loc, Reason: "r", ApprovedBy: "shopping-discovery",
		Expires: config.Date{Time: time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC)}}
	yes := func(string) (bool, error) { return true, nil }
	no := func(string) (bool, error) { return false, nil }
	for _, c := range []struct {
		now  time.Time
		appr Approver
		want WaiverStatus
		exit int
	}{
		{time.Date(2026, 12, 15, 23, 0, 0, 0, time.UTC), yes, Applied, 0},
		{time.Date(2026, 12, 16, 0, 0, 1, 0, time.UTC), yes, Expired, 1},
		{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), no, Unapproved, 1},
		{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), nil, Unapproved, 1}, // no MR/PR context
	} {
		fs := run(t, baseSpec(t), "selling_price: {type: integer, format: int64}", "selling_price: {type: number, format: double}")
		if _, err := ApplyWaivers(fs, []config.Waiver{w}, c.now, c.appr); err != nil {
			t.Fatal(err)
		}
		exit, _ := Gate(fs, "ERR", "block")
		var got WaiverStatus
		for _, f := range fs {
			if f.Location == loc {
				got = f.Waived
			}
		}
		if got != c.want || exit != c.exit {
			t.Errorf("now=%s: got %s exit %d, want %s exit %d", c.now, got, exit, c.want, c.exit)
		}
	}
}

func TestReports(t *testing.T) {
	fs := run(t, baseSpec(t), "selling_price: {type: integer, format: int64}", "selling_price: {type: number, format: double}")
	_, blocking := Gate(fs, "ERR", "warn")
	Sort(fs)
	md := Markdown(ReportMeta{Service: "product-detail", Mode: "warn", EffectiveMode: "warn", WarnUntil: "2026-12-01", Blocking: blocking}, fs)
	if !strings.Contains(md, "WOULD BLOCK after 2026-12-01") || !strings.Contains(md, "`POST /v1/products/ 200 /items/selling_price`") {
		t.Errorf("markdown:\n%s", md)
	}
	x, err := JUnit("product-detail", fs)
	if err != nil || !strings.Contains(string(x), `<failure message="ERR response-property-type-generalized">`) {
		t.Errorf("junit %v:\n%s", err, x)
	}
}

// TestRuleInventory forces a decision for every rule a new oasdiff release adds.
func TestRuleInventory(t *testing.T) {
	known := map[string]bool{}
	for _, id := range strings.Fields(string(mustRead(t, "policy/known-rules.txt"))) {
		known[id] = true
	}
	var p policy
	if err := yaml.Unmarshal(policyYAML, &p); err != nil {
		t.Fatal(err)
	}
	for _, id := range p.Probation {
		known[id] = true
	}
	for _, id := range checker.GetAllRuleIds() {
		if !known[id] {
			t.Errorf("new oasdiff rule %s: add it to policy/severity.yaml probation (WARN for one release)", id)
		}
	}
}
