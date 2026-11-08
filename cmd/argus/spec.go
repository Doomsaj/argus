package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"

	"git.digikala.com/argus/argus/internal/config"
	"git.digikala.com/argus/argus/internal/diff"
	"git.digikala.com/argus/argus/internal/github"
	"git.digikala.com/argus/argus/internal/gitlab"
	"git.digikala.com/argus/argus/internal/lint"
	"git.digikala.com/argus/argus/internal/pactbroker"
	"git.digikala.com/argus/argus/internal/registry"
)

var teamsFn = config.Teams // replaced in tests

func specLint(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("spec lint", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return errors.Join(err, errors.New("need exactly one spec file"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	doc, _, err := loadSpec(ctx, pos[0])
	if err != nil {
		return err
	}
	findings := lint.Lint(doc)
	for _, f := range findings {
		fmt.Printf("%s %s %s\n", f.Rule, f.Pointer, f.Message)
	}
	fmt.Printf("argus spec lint: %d findings\n", len(findings))
	if len(findings) > 0 && cfg.Gate.EffectiveMode(now()) == "block" {
		return fmt.Errorf("%w: %d lint findings", errGate, len(findings))
	}
	return nil
}

func specCheck(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("spec check", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	against := fs.String("against", "production", "Apicurio branch to diff against: production|staging|main")
	baseFile := fs.String("base", "", "diff against a local file instead of Apicurio (local runs, tests)")
	mdOut := fs.String("md", "argus-report.md", "markdown report path")
	junitOut := fs.String("junit", "argus-report.junit.xml", "JUnit report path")
	noNote := fs.Bool("no-note", false, "do not post the MR note / PR comment")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return errors.Join(err, errors.New("need exactly one spec file"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	env := config.FromEnv()
	mode := cfg.Gate.EffectiveMode(now())
	_, revision, err := loadSpec(ctx, pos[0])
	if err != nil {
		return err
	}

	// 1. Baseline: tip of the env branch; production falls back to main until the first deploy.
	meta := diff.ReportMeta{Service: cfg.Service, Against: *against, Mode: cfg.Gate.Mode, EffectiveMode: mode,
		WarnUntil: cfg.Gate.WarnUntil.Format("2006-01-02")}
	meta.RevisionVersion, _ = env.ShortSHA()
	var base []byte
	if *baseFile != "" {
		meta.BaseVersion = *baseFile
		if base, err = os.ReadFile(*baseFile); err != nil {
			return err
		}
	} else {
		reg, err := registryClient(env)
		if err != nil {
			return soft(mode, err)
		}
		meta.BaseVersion, base, err = reg.BranchTip(ctx, cfg.Group, cfg.Service, *against)
		if errors.Is(err, registry.ErrNotFound) && *against == "production" {
			meta.Against = "main (never deployed to production)"
			meta.BaseVersion, base, err = reg.BranchTip(ctx, cfg.Group, cfg.Service, "main")
		}
		if errors.Is(err, registry.ErrNotFound) {
			fmt.Println("argus spec check: no baseline in Apicurio (first publish), nothing to compare")
			return nil
		}
		if err != nil {
			return soft(mode, err)
		}
	}

	// 2. oasdiff with platform severity policy.
	findings, err := diff.Breaking(base, revision)
	if err != nil {
		return err
	}

	// 3. Waivers: applied only if the approvedBy team approved the MR (GitLab) or PR (GitHub).
	var approver diff.Approver
	var gl *gitlab.Client
	var gh *github.Client
	if env.GitLabToken != "" && env.GitLabAPI != "" {
		gl = gitlab.New(env.GitLabAPI, env.GitLabToken, env.ProjectID)
	}
	if env.CI == "github" && env.GitHubToken != "" && env.Repository != "" {
		gh = github.New(env.GitHubAPI, env.GitHubToken, env.Repository)
	}
	if len(cfg.Waivers) > 0 && (gl != nil || gh != nil) {
		teams, err := teamsFn()
		if err != nil {
			return err
		}
		if gh != nil {
			approver, err = gh.Approver(ctx, env.PRNumber, env.CommitSHA, teams)
		} else {
			approver, err = gl.Approver(ctx, env.MRIID, env.CommitSHA, teams)
		}
		if err != nil {
			return soft(mode, err)
		}
	}
	unused, err := diff.ApplyWaivers(findings, cfg.Waivers, now(), approver)
	if err != nil {
		return soft(mode, err)
	}
	for _, w := range unused {
		meta.UnusedWaivers = append(meta.UnusedWaivers, w.Rule+" "+w.Path)
	}

	// 4. Gate.
	exit, blocking := diff.Gate(findings, cfg.Gate.FailOn, mode)
	meta.Blocking, meta.Exit = blocking, exit
	diff.Sort(findings)

	// 5. Who is affected (best effort, RO broker user).
	if env.BrokerURL != "" {
		cs, err := pactbroker.NewHTTP(env.BrokerURL, env.BrokerUser, env.BrokerPassword).Consumers(ctx, cfg.Pact.ProviderName)
		if err != nil {
			meta.ConsumersUnavailable = true
			fmt.Fprintln(os.Stderr, "argus: consumers lookup failed:", err)
		}
		for i := range findings {
			findings[i].Consumers = pactbroker.Affected(cs, findings[i].Operation, findings[i].Path)
		}
	}

	// 6. Reports. The MR note never fails the job.
	md := diff.Markdown(meta, findings)
	junit, err := diff.JUnit(cfg.Service, findings)
	if err != nil {
		return err
	}
	if err := errors.Join(os.WriteFile(*mdOut, []byte(md), 0o644), os.WriteFile(*junitOut, junit, 0o644)); err != nil {
		return err
	}
	fmt.Print(md)
	if gl != nil && env.MRIID != "" && !*noNote {
		if err := gl.UpsertMRNote(ctx, env.MRIID, md); err != nil {
			fmt.Fprintln(os.Stderr, "argus: MR note failed (not fatal):", err)
		}
	}
	if env.StepSummary != "" {
		if err := github.AppendStepSummary(env.StepSummary, md); err != nil {
			fmt.Fprintln(os.Stderr, "argus: step summary failed (not fatal):", err)
		}
	}
	if gh != nil && env.PRNumber != "" && !*noNote {
		if err := gh.UpsertPRComment(ctx, env.PRNumber, md); err != nil {
			fmt.Fprintln(os.Stderr, "argus: PR comment failed (not fatal):", err)
		}
	}
	if exit != 0 {
		return fmt.Errorf("%w: %d blocking findings", errGate, blocking)
	}
	return nil
}

func specPublish(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("spec publish", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	branch := fs.String("branch", "", "Apicurio branch to append to (main)")
	dryRun := fs.Bool("dry-run", false, "validate against Apicurio rules without storing")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 || *branch == "" {
		return errors.Join(err, errors.New("need one spec file and --branch"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	env := config.FromEnv()
	_, raw, err := loadSpec(ctx, pos[0])
	if err != nil {
		return err
	}
	ver, err := env.ShortSHA()
	if err != nil {
		return err
	}
	reg, err := registryClient(env)
	if err != nil {
		return err
	}
	labels := map[string]string{
		"git.branch": env.RefName, "git.sha": env.CommitSHA,
		env.PipelineLabel(): env.PipelineID, "spec.generator": cfg.Spec.Generator,
	}
	if err := reg.CreateVersion(ctx, cfg.Group, cfg.Service, ver, raw, labels, *dryRun); err != nil {
		return fmt.Errorf("%w: %v", errDep, err)
	}
	if *dryRun {
		fmt.Printf("argus spec publish: dry run ok for %s/%s@%s\n", cfg.Group, cfg.Service, ver)
		return nil
	}
	if err := reg.AddToBranch(ctx, cfg.Group, cfg.Service, *branch, ver); err != nil {
		return fmt.Errorf("%w: %v", errDep, err)
	}
	// Register the provider version in the broker so can-i-deploy knows it even without pacts.
	if env.BrokerURL != "" {
		if err := brokerCLI(env).CreateVersion(ctx, cfg.Pact.ProviderName, env.CommitSHA, env.RefName); err != nil {
			return fmt.Errorf("%w: pact-broker create-or-update-version: %v", errDep, err)
		}
	}
	fmt.Printf("argus spec publish: %s/%s@%s on branch %s\n", cfg.Group, cfg.Service, ver, *branch)
	return nil
}
