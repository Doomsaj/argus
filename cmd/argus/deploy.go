package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"

	"git.digikala.com/argus/argus/internal/config"
	"git.digikala.com/argus/argus/internal/github"
	"git.digikala.com/argus/argus/internal/gitlab"
	"git.digikala.com/argus/argus/internal/pactbroker"
)

var breakGlass = regexp.MustCompile(`^\S+: \S.{4,}$`) // "<incident-id>: <reason>"

func canIDeploy(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("can-i-deploy", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	to := fs.String("to", "", "target environment: staging|production")
	ver := fs.String("version", "", "full commit SHA to check (default $CI_COMMIT_SHA)")
	if _, err := parse(fs, args); err != nil || *to == "" {
		return errors.Join(err, errors.New("--to is required"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	env := config.FromEnv()
	if *ver != "" {
		env.CommitSHA = *ver // release-MR / argo-revision mode: the merge commit's source parent
	}
	mode := cfg.Gate.EffectiveMode(now())
	if err := config.Require("CI_COMMIT_SHA", env.CommitSHA, "PACT_BROKER_BASE_URL", env.BrokerURL); err != nil {
		return err
	}

	var reasons []error
	// 1. Provider: the exact spec of this commit must be in Apicurio (published by the default-branch pipeline).
	if cfg.Has("provider") {
		ver, err := env.ShortSHA()
		if err != nil {
			return err
		}
		reg, err := registryClient(env)
		if err != nil {
			return err
		}
		ok, err := reg.VersionExists(ctx, cfg.Group, cfg.Service, ver)
		switch {
		case err != nil:
			reasons = append(reasons, fmt.Errorf("%w: apicurio: %v", errDep, err))
		case !ok:
			reasons = append(reasons, fmt.Errorf("%w: spec %s/%s@%s not in Apicurio; deploy only commits built by the default-branch pipeline", errGate, cfg.Group, cfg.Service, ver))
		}
	}
	// 2. Pact matrix: every consumer/provider version already in <to> is compatible with this one.
	name := cfg.Pact.ProviderName
	if name == "" {
		name = cfg.Pact.ConsumerName
	}
	if err := brokerCLI(env).CanIDeploy(ctx, name, env.CommitSHA, *to); errors.Is(err, pactbroker.ErrNotRun) {
		reasons = append(reasons, fmt.Errorf("%w: %v", errDep, err))
	} else if err != nil {
		reasons = append(reasons, fmt.Errorf("%w: pact-broker can-i-deploy: %v", errGate, err))
	}
	if len(reasons) == 0 {
		fmt.Printf("argus can-i-deploy: %s@%s to %s: yes\n", cfg.Service, env.CommitSHA, *to)
		return nil
	}
	verdict := errors.Join(reasons...)

	// 3. Break-glass and warn mode print the verdict and let the deploy continue.
	if env.BreakGlass != "" {
		if !breakGlass.MatchString(env.BreakGlass) {
			return fmt.Errorf("ARGUS_BREAK_GLASS must look like \"<incident-id>: <reason>\", got %q", env.BreakGlass)
		}
		audit := fmt.Sprintf("ARGUS_AUDIT break_glass service=%s version=%s env=%s pipeline=%s reason=%q verdict=%q",
			cfg.Service, env.CommitSHA, *to, env.PipelineID, env.BreakGlass, verdict.Error())
		fmt.Fprintln(os.Stderr, audit)
		var noteErr error
		switch {
		case env.CI == "github" && env.GitHubToken != "":
			noteErr = github.New(env.GitHubAPI, env.GitHubToken, env.Repository).CommitComment(ctx, env.CommitSHA, "argus break-glass deploy: "+audit)
		case env.GitLabToken != "":
			noteErr = gitlab.New(env.GitLabAPI, env.GitLabToken, env.ProjectID).CommitNote(ctx, env.CommitSHA, "argus break-glass deploy: "+audit)
		}
		if noteErr != nil {
			fmt.Fprintln(os.Stderr, "argus: audit note failed:", noteErr)
		}
		return nil
	}
	if mode != "block" {
		fmt.Fprintf(os.Stderr, "argus can-i-deploy: WOULD BLOCK after %s: %v\n", cfg.Gate.WarnUntil.Format("2006-01-02"), verdict)
		return nil
	}
	return verdict // fail closed: exit 1 if any gate reason, else 3
}

func deployRecord(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("deploy record", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	envName := fs.String("env", "", "environment that now runs this commit")
	ver := fs.String("version", "", "full commit SHA that was deployed (default $CI_COMMIT_SHA)")
	if _, err := parse(fs, args); err != nil || *envName == "" {
		return errors.Join(err, errors.New("--env is required"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	env := config.FromEnv()
	if *ver != "" {
		env.CommitSHA = *ver
	}
	if cfg.Has("provider") {
		ver, err := env.ShortSHA()
		if err != nil {
			return err
		}
		reg, err := registryClient(env)
		if err != nil {
			return err
		}
		// The env branch tip is "what is live"; spec check of the next MR diffs against it.
		if err := reg.AddToBranch(ctx, cfg.Group, cfg.Service, *envName, ver); err != nil {
			return fmt.Errorf("%w: apicurio branch %s: %v", errDep, *envName, err)
		}
	}
	name := cfg.Pact.ProviderName
	if name == "" {
		name = cfg.Pact.ConsumerName
	}
	if err := brokerCLI(env).RecordDeployment(ctx, name, env.CommitSHA, *envName); err != nil {
		return fmt.Errorf("%w: pact-broker record-deployment: %v", errDep, err)
	}
	return nil
}
