// Package diff runs oasdiff (as a library) and applies argus policy:
// platform severity overrides, waivers and the warn/block gate.
package diff

import (
	_ "embed"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oasdiff/oasdiff/checker"
	oasdiff "github.com/oasdiff/oasdiff/diff"
	"github.com/oasdiff/oasdiff/load"
	"gopkg.in/yaml.v3"

	"git.digikala.com/argus/argus/internal/config"
)

//go:embed policy/severity.yaml
var policyYAML []byte

type policy struct {
	Overrides map[string]string `yaml:"overrides"`
	Probation []string          `yaml:"probation"`
}

// severityLevels turns the embedded policy into oasdiff overrides.
func severityLevels() (map[string]checker.Level, error) {
	var p policy
	if err := yaml.Unmarshal(policyYAML, &p); err != nil {
		return nil, err
	}
	levels := map[string]checker.Level{}
	for id, l := range p.Overrides {
		lv, err := checker.NewLevel(l)
		if err != nil {
			return nil, fmt.Errorf("policy: %s: %w", id, err)
		}
		levels[id] = lv
	}
	for _, id := range p.Probation {
		levels[id] = checker.WARN
	}
	return levels, nil
}

// Finding is one oasdiff change plus argus decisions.
type Finding struct {
	Rule        string // oasdiff rule id, e.g. response-property-type-generalized
	Level       string // ERR | WARN | INFO after overrides
	Operation   string // POST
	Path        string // /v1/products/
	OperationID string
	Location    string // waiver key: "POST /v1/products/ 200 /items/selling_price"
	Text        string // oasdiff message
	Waiver      *config.Waiver
	Waived      WaiverStatus
	Blocking    bool
	Consumers   []string // filled from the Pact Broker, see pactbroker.Consumers
}

type WaiverStatus string

const (
	NotWaived  WaiverStatus = ""
	Applied    WaiverStatus = "APPLIED"
	Expired    WaiverStatus = "EXPIRED"
	Unapproved WaiverStatus = "UNAPPROVED"
)

// Breaking diffs base (what is live) against revision (this commit).
func Breaking(base, revision []byte) ([]Finding, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false // specs are self-contained build artifacts
	b, err := load.NewSpecInfoFromData(loader, base, "base", load.WithFlattenAllOf())
	if err != nil {
		return nil, fmt.Errorf("load base spec: %w", err)
	}
	r, err := load.NewSpecInfoFromData(loader, revision, "revision", load.WithFlattenAllOf())
	if err != nil {
		return nil, fmt.Errorf("load revision spec: %w", err)
	}
	d, sources, err := oasdiff.GetWithOperationsSourcesMap(oasdiff.NewConfig(), b, r)
	if err != nil {
		return nil, fmt.Errorf("oasdiff: %w", err)
	}
	levels, err := severityLevels()
	if err != nil {
		return nil, err
	}
	cfg := checker.NewConfig(checker.GetAllChecks(), checker.WithSeverityLevels(levels))
	l := checker.NewDefaultLocalizer()
	var out []Finding
	// INFO is kept so the MR report can show non-breaking changes in a collapsed section.
	for _, c := range checker.CheckBackwardCompatibilityUntilLevel(cfg, d, sources, checker.INFO) {
		out = append(out, Finding{
			Rule:        c.GetId(),
			Level:       levelName[c.GetLevel()],
			Operation:   c.GetOperation(),
			Path:        c.GetPath(),
			OperationID: c.GetOperationId(),
			Location:    location(c),
			Text:        c.GetUncolorizedText(l),
		})
	}
	return out, nil
}

// location builds the waiver key "<METHOD> <path> <status|request> /<property path>".
// oasdiff does not export the property path, so this relies on its argument order:
// args[0] = property path (args[1] for enum-value rules), last arg = response status.
// shortcut: argument order is not an oasdiff API; golden tests pin it, re-check on every oasdiff bump.
func location(c checker.Change) string {
	if c.GetOperation() == "" {
		return c.GetPath() // component-level change
	}
	parts := []string{c.GetOperation(), c.GetPath()}
	args := c.GetArgs()
	id := c.GetId()
	switch {
	case strings.HasPrefix(id, "response-") && len(args) > 0:
		if s, ok := args[len(args)-1].(string); ok && len(s) == 3 && s[0] >= '1' && s[0] <= '5' {
			parts = append(parts, s)
		}
	case strings.HasPrefix(id, "request-"):
		parts = append(parts, "request")
	}
	i := 0
	if strings.Contains(id, "property-enum-value") {
		i = 1 // enum rules put the enum value first: [value, property, status]
	}
	if strings.Contains(id, "property") && len(args) > i {
		if p, ok := args[i].(string); ok {
			parts = append(parts, "/"+p)
		}
	}
	return strings.Join(parts, " ")
}

// Approver reports whether team approved the change (GitLab MR approvals); nil means
// "no MR context" (local run), in which case waivers are not applied.
type Approver func(team string) (bool, error)

// ApplyWaivers marks findings waived and returns waivers that matched nothing (for cleanup).
func ApplyWaivers(fs []Finding, ws []config.Waiver, now time.Time, approved Approver) (unused []config.Waiver, err error) {
	used := make([]bool, len(ws))
	for i := range fs {
		for j := range ws {
			w := &ws[j]
			if w.Rule != fs[i].Rule || w.Path != fs[i].Location {
				continue
			}
			used[j] = true
			fs[i].Waiver = w
			switch ok, aerr := approvedOrFalse(approved, w.ApprovedBy); {
			case aerr != nil:
				return nil, aerr
			case now.UTC().After(w.Expires.AddDate(0, 0, 1)):
				fs[i].Waived = Expired
			case !ok:
				fs[i].Waived = Unapproved
			default:
				fs[i].Waived = Applied
			}
			break
		}
	}
	for j, u := range used {
		if !u {
			unused = append(unused, ws[j])
		}
	}
	return unused, nil
}

func approvedOrFalse(a Approver, team string) (bool, error) {
	if a == nil {
		return false, nil
	}
	return a(team)
}

var (
	rank      = map[string]int{"INFO": 0, "WARN": 1, "ERR": 2}
	levelName = map[checker.Level]string{checker.INFO: "INFO", checker.WARN: "WARN", checker.ERR: "ERR"}
)

// Gate sets Blocking and returns the process exit code for spec check (0 or 1).
func Gate(fs []Finding, failOn, effectiveMode string) (exit int, blocking int) {
	for i := range fs {
		f := &fs[i]
		f.Blocking = rank[f.Level] >= rank[failOn] && f.Waived != Applied
		if f.Blocking {
			blocking++
		}
	}
	if blocking > 0 && effectiveMode == "block" {
		return 1, blocking
	}
	return 0, blocking
}

// Sort puts blocking ERR first so the MR note leads with what matters.
func Sort(fs []Finding) {
	slices.SortStableFunc(fs, func(a, b Finding) int {
		if a.Blocking != b.Blocking {
			if a.Blocking {
				return -1
			}
			return 1
		}
		if d := rank[b.Level] - rank[a.Level]; d != 0 {
			return d
		}
		return strings.Compare(a.Location, b.Location)
	})
}
