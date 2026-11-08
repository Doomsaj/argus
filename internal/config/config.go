// Package config loads and validates argus.yaml and the ARGUS_* environment.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"time"

	"gopkg.in/yaml.v3"
)

// Config mirrors argus.yaml. Unknown keys are an error (typos must not silently disable a gate).
type Config struct {
	Service string   `yaml:"service"`
	Group   string   `yaml:"group"`
	Role    []string `yaml:"role"`
	Spec    Spec     `yaml:"spec"`
	Gate    Gate     `yaml:"gate"`
	Waivers []Waiver `yaml:"waivers"`
	Pact    Pact     `yaml:"pact"`
	Samples Samples  `yaml:"samples"`
}

type Spec struct {
	Path      string `yaml:"path"`
	Generator string `yaml:"generator"`
}

type Gate struct {
	Mode      string `yaml:"mode"`      // warn | block
	WarnUntil Date   `yaml:"warnUntil"` // last day of warn mode (UTC)
	FailOn    string `yaml:"failOn"`    // ERR | WARN
}

type Waiver struct {
	Rule       string `yaml:"rule"`
	Path       string `yaml:"path"`
	Reason     string `yaml:"reason"`
	ApprovedBy string `yaml:"approvedBy"`
	Expires    Date   `yaml:"expires"`
}

type Pact struct {
	ProviderName string `yaml:"providerName"`
	ConsumerName string `yaml:"consumerName"`
}

type Samples struct {
	Dir string `yaml:"dir"`
}

// Date is a calendar day (YYYY-MM-DD, UTC). yaml.v3 would otherwise turn it into a timestamp.
type Date struct{ time.Time }

func (d *Date) UnmarshalYAML(n *yaml.Node) error {
	t, err := time.Parse(time.DateOnly, n.Value)
	if err != nil {
		return fmt.Errorf("line %d: want YYYY-MM-DD, got %q", n.Line, n.Value)
	}
	d.Time = t
	return nil
}

func (c *Config) Has(role string) bool {
	return slices.Contains(c.Role, role) || slices.Contains(c.Role, "both")
}

// EffectiveMode applies warnUntil: warn turns into block the day after warnUntil (UTC).
func (g Gate) EffectiveMode(now time.Time) string {
	if g.Mode == "warn" && !g.WarnUntil.IsZero() && !now.UTC().Before(g.WarnUntil.AddDate(0, 0, 1)) {
		return "block"
	}
	return g.Mode
}

var (
	slug       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)
	generators = []string{"springdoc", "scramble", "nelmio", "huma", "swaggo", "other"}
)

const (
	MaxWaiverDays    = 30
	MaxWarnDaysAhead = 28 // default onboarding+14 plus one 14-day extension
)

// Load reads path (default argus.yaml), applies defaults and validates against now.
func Load(path string, now time.Time) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	c.defaults()
	if err := c.Validate(now); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Gate.Mode == "" {
		c.Gate.Mode = "warn"
	}
	if c.Gate.FailOn == "" {
		c.Gate.FailOn = "ERR"
	}
	if c.Pact.ProviderName == "" && c.Has("provider") {
		c.Pact.ProviderName = c.Service
	}
	if c.Pact.ConsumerName == "" && c.Has("consumer") {
		c.Pact.ConsumerName = c.Service
	}
	if c.Samples.Dir == "" {
		c.Samples.Dir = "target/argus-samples"
	}
}

// Validate returns every problem at once so one pipeline run shows all of them.
func (c *Config) Validate(now time.Time) error {
	var errs []error
	bad := func(f string, a ...any) { errs = append(errs, fmt.Errorf(f, a...)) }
	today := now.UTC().Truncate(24 * time.Hour)

	if !slug.MatchString(c.Service) {
		bad("service: %q must be a k8s-style name (%s)", c.Service, slug)
	}
	if !slug.MatchString(c.Group) {
		bad("group: %q must be a team slug (%s)", c.Group, slug)
	}
	if len(c.Role) == 0 {
		bad("role: at least one of provider, consumer, both")
	}
	for _, r := range c.Role {
		if !slices.Contains([]string{"provider", "consumer", "both"}, r) {
			bad("role: unknown %q", r)
		}
	}
	if c.Has("provider") {
		if c.Spec.Path == "" {
			bad("spec.path: required for role provider")
		}
		if !slices.Contains(generators, c.Spec.Generator) {
			bad("spec.generator: %q not in %v", c.Spec.Generator, generators)
		}
		if c.Pact.ProviderName != c.Service {
			bad("pact.providerName: must equal service %q (one name everywhere)", c.Service)
		}
	}
	if c.Has("consumer") && c.Pact.ConsumerName != c.Service {
		bad("pact.consumerName: must equal service %q", c.Service)
	}
	switch c.Gate.Mode {
	case "block":
	case "warn":
		if c.Gate.WarnUntil.IsZero() {
			bad("gate.warnUntil: required when gate.mode is warn")
		} else if c.Gate.WarnUntil.Sub(today) > MaxWarnDaysAhead*24*time.Hour {
			bad("gate.warnUntil: %s is more than %d days ahead", c.Gate.WarnUntil.Format(time.DateOnly), MaxWarnDaysAhead)
		}
	default:
		bad("gate.mode: %q not in [warn block]", c.Gate.Mode)
	}
	if c.Gate.FailOn != "ERR" && c.Gate.FailOn != "WARN" {
		bad("gate.failOn: %q not in [ERR WARN]", c.Gate.FailOn)
	}
	for i, w := range c.Waivers {
		p := fmt.Sprintf("waivers[%d]", i)
		if w.Rule == "" || w.Path == "" || w.Reason == "" || w.ApprovedBy == "" || w.Expires.IsZero() {
			bad("%s: rule, path, reason, approvedBy, expires are all required", p)
		}
		if !slug.MatchString(w.ApprovedBy) {
			bad("%s.approvedBy: %q is not a team slug", p, w.ApprovedBy)
		}
		if w.ApprovedBy == c.Group {
			bad("%s.approvedBy: a provider team cannot approve its own waiver", p)
		}
		if w.Expires.Sub(today) > MaxWaiverDays*24*time.Hour {
			bad("%s.expires: more than %d days ahead", p, MaxWaiverDays)
		}
	}
	return errors.Join(errs...)
}
