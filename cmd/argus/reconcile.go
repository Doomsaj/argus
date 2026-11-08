package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"git.digikala.com/argus/argus/internal/config"
	"git.digikala.com/argus/argus/internal/pactbroker"
	"git.digikala.com/argus/argus/internal/registry"
)

// Mismatch is one reconciliation finding. Kinds:
//
//	only-in-apicurio   spec exists, broker has no pacticipant of that name
//	only-in-broker     pacticipant without spec (normal for consumer-only services: allow-list them)
//	near-duplicate     names equal after lower-casing and '_' -> '-'
//	deployed-differs   Apicurio <env> branch tip git.sha != broker currently-deployed version
//	no-spec-deployed   broker says a SHA is deployed, Apicurio has no version for it
type Mismatch struct {
	Kind    string `json:"kind" yaml:"kind"`
	Service string `json:"service" yaml:"service"`
	Env     string `json:"env,omitempty" yaml:"env,omitempty"`
	Detail  string `json:"detail,omitempty" yaml:"-"`
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func reconcile(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("reconcile", flag.ContinueOnError)
	var envs multiFlag
	fs.Var(&envs, "env", "environment to reconcile (repeatable)")
	allowPath := fs.String("allow-list", "", "YAML list of known exceptions: [{kind, service, env}]")
	out := fs.String("out", "reconcile-report", "report path prefix (.json and .md are added)")
	if _, err := parse(fs, args); err != nil || len(envs) == 0 {
		return errors.Join(err, errors.New("at least one --env"))
	}
	var allow []Mismatch
	if *allowPath != "" {
		raw, err := os.ReadFile(*allowPath)
		if err != nil {
			return err
		}
		if err := yaml.Unmarshal(raw, &allow); err != nil {
			return fmt.Errorf("%s: %w", *allowPath, err)
		}
	}
	env := config.FromEnv()
	reg, err := registryClient(env)
	if err != nil {
		return err
	}
	if err := config.Require("PACT_BROKER_BASE_URL", env.BrokerURL); err != nil {
		return err
	}
	broker := pactbroker.NewHTTP(env.BrokerURL, env.BrokerUser, env.BrokerPassword)

	found, err := reconcileAll(ctx, reg, broker, envs)
	if err != nil {
		return fmt.Errorf("%w: %v", errDep, err)
	}
	var report []Mismatch
	for _, m := range found {
		if !slices.ContainsFunc(allow, func(a Mismatch) bool {
			return a.Kind == m.Kind && a.Service == m.Service && (a.Env == "" || a.Env == m.Env)
		}) {
			report = append(report, m)
		}
	}
	js, _ := json.MarshalIndent(report, "", "  ")
	var md strings.Builder
	fmt.Fprintf(&md, "### argus reconcile: %d mismatches\n\n| Kind | Service | Env | Detail |\n|---|---|---|---|\n", len(report))
	for _, m := range report {
		fmt.Fprintf(&md, "| %s | %s | %s | %s |\n", m.Kind, m.Service, m.Env, m.Detail)
	}
	if err := errors.Join(os.WriteFile(*out+".json", js, 0o644), os.WriteFile(*out+".md", []byte(md.String()), 0o644)); err != nil {
		return err
	}
	fmt.Print(md.String())
	fmt.Printf("argus_reconcile_mismatches %d\n", len(report)) // scraped from the job log / pushed by the job
	if len(report) > 0 {
		return fmt.Errorf("%w: %d reconciliation mismatches", errGate, len(report))
	}
	return nil
}

func reconcileAll(ctx context.Context, reg *registry.Client, broker *pactbroker.HTTP, envs []string) ([]Mismatch, error) {
	arts, err := reg.ListArtifacts(ctx)
	if err != nil {
		return nil, err
	}
	names, err := broker.Pacticipants(ctx)
	if err != nil {
		return nil, err
	}
	var out []Mismatch
	group := map[string]string{} // artifactId -> groupId
	for _, a := range arts {
		group[a.ArtifactID] = a.GroupID
		if !slices.Contains(names, a.ArtifactID) {
			out = append(out, Mismatch{Kind: "only-in-apicurio", Service: a.ArtifactID})
		}
	}
	norm := map[string][]string{}
	for _, n := range names {
		if _, ok := group[n]; !ok {
			out = append(out, Mismatch{Kind: "only-in-broker", Service: n})
		}
		k := strings.ReplaceAll(strings.ToLower(n), "_", "-")
		norm[k] = append(norm[k], n)
	}
	for _, a := range arts {
		k := strings.ReplaceAll(strings.ToLower(a.ArtifactID), "_", "-")
		norm[k] = append(norm[k], a.ArtifactID)
	}
	for _, v := range norm {
		slices.Sort(v)
		if v = slices.Compact(v); len(v) > 1 {
			out = append(out, Mismatch{Kind: "near-duplicate", Service: v[0], Detail: strings.Join(v, ", ")})
		}
	}
	for _, e := range envs {
		deployed, err := broker.CurrentlyDeployed(ctx, e)
		if err != nil {
			return nil, err
		}
		for svc, shas := range deployed {
			g, isProvider := group[svc]
			if !isProvider {
				continue // consumer-only services have no spec
			}
			tip, _, err := reg.BranchTip(ctx, g, svc, e)
			if err != nil && !errors.Is(err, registry.ErrNotFound) {
				return nil, err
			}
			var tipSHA string
			if tip != "" {
				labels, err := reg.VersionLabels(ctx, g, svc, tip)
				if err != nil {
					return nil, err
				}
				tipSHA = labels["git.sha"]
			}
			for _, sha := range shas {
				if len(sha) < 12 {
					continue
				}
				ok, err := reg.VersionExists(ctx, g, svc, sha[:12])
				if err != nil {
					return nil, err
				}
				if !ok {
					out = append(out, Mismatch{Kind: "no-spec-deployed", Service: svc, Env: e, Detail: sha})
				}
			}
			if !slices.Contains(shas, tipSHA) {
				out = append(out, Mismatch{Kind: "deployed-differs", Service: svc, Env: e,
					Detail: fmt.Sprintf("apicurio %s tip git.sha=%q, broker deployed %v", e, tipSHA, shas)})
			}
		}
	}
	slices.SortFunc(out, func(a, b Mismatch) int {
		return strings.Compare(a.Kind+a.Service+a.Env, b.Kind+b.Service+b.Env)
	})
	return out, nil
}
