package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	"git.digikala.com/argus/argus/internal/config"
	"git.digikala.com/argus/argus/internal/pactbroker"
	"git.digikala.com/argus/argus/internal/samples"
)

func verifySamples(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("verify samples", flag.ContinueOnError)
	cfgPath := fs.String("config", "argus.yaml", "argus config file")
	spec := fs.String("spec", "", "OpenAPI file")
	dir := fs.String("dir", "", "samples dir (default from argus.yaml samples.dir)")
	if _, err := parse(fs, args); err != nil || *spec == "" {
		return errors.Join(err, errors.New("--spec is required"))
	}
	cfg, err := loadConfig(*cfgPath)
	if err != nil {
		return err
	}
	if *dir == "" {
		*dir = cfg.Samples.Dir
	}
	doc, _, err := loadSpec(ctx, *spec)
	if err != nil {
		return err
	}
	rep, err := samples.Run(doc, *dir)
	if err != nil {
		return err
	}
	for _, r := range rep.Results {
		fmt.Printf("%s %s %s#%s %s\n", r.Kind, r.OperationID, r.File, r.Pointer, r.Detail)
	}
	fmt.Printf("argus verify samples: %d files, %d problems, operations without samples: %v\n", rep.Files, len(rep.Results), rep.Uncovered)
	bad := len(rep.Results)
	if rep.Files == 0 {
		fmt.Fprintln(os.Stderr, "argus: no samples found; the provider tests must dump responses to", *dir)
		bad++
	}
	if bad > 0 && cfg.Gate.EffectiveMode(now()) == "block" {
		return fmt.Errorf("%w: %d sample problems", errGate, bad)
	}
	return nil
}

func consumers(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("consumers", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	pos, err := parse(fs, args)
	if err != nil || len(pos) != 1 {
		return errors.Join(err, errors.New("need exactly one service name"))
	}
	env := config.FromEnv()
	if err := config.Require("PACT_BROKER_BASE_URL", env.BrokerURL); err != nil {
		return err
	}
	cs, err := pactbroker.NewHTTP(env.BrokerURL, env.BrokerUser, env.BrokerPassword).Consumers(ctx, pos[0])
	if err != nil {
		return fmt.Errorf("%w: %v", errDep, err)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(cs)
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "CONSUMER\tENDPOINT")
	for _, c := range cs {
		for _, e := range c.Endpoints {
			fmt.Fprintf(w, "%s\t%s\n", c.Name, e)
		}
	}
	return w.Flush()
}
