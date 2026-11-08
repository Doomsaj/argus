// Command argus is the CI glue between provider/consumer pipelines, Apicurio Registry,
// oasdiff and the Pact Broker. See docs/06-argus-cli.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"slices"
	"strings"
	"time"
)

var version = "dev" // -ldflags "-X main.version=v1.2.3"

// Exit codes are part of the CI contract (07-ci-templates.md relies on them).
const (
	exitOK     = 0 // success, warn mode, or break-glass
	exitGate   = 1 // gate said no in effective block mode
	exitUsage  = 2 // bad flags, bad argus.yaml, unreadable spec
	exitDepend = 3 // Apicurio / Pact Broker / OIDC unreachable or rejected the call
)

// errGate and errDep classify failures; everything else is a usage error.
var (
	errGate = errors.New("gate")
	errDep  = errors.New("dependency")
)

type command struct {
	name, usage string
	run         func(ctx context.Context, args []string) error
}

func commands() []command {
	return []command{
		{"spec lint", "argus spec lint <file>", specLint},
		{"spec check", "argus spec check <file> [--against production|staging|main] [--base file]", specCheck},
		{"spec publish", "argus spec publish <file> --branch main", specPublish},
		{"deploy record", "argus deploy record --env staging|production [--version sha]", deployRecord},
		{"can-i-deploy", "argus can-i-deploy --to staging|production [--version sha]", canIDeploy},
		{"verify samples", "argus verify samples --spec <file> --dir <samples-dir>", verifySamples},
		{"consumers", "argus consumers <service> [--json]", consumers},
		{"reconcile", "argus reconcile --env staging --env production [--allow-list reconcile.yaml] [--out reconcile-report]", reconcile},
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute) // can-i-deploy retries up to ~2 min
	defer cancel()
	os.Exit(dispatch(ctx, os.Args[1:]))
}

func dispatch(ctx context.Context, args []string) int {
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println(version)
		return exitOK
	}
	for _, c := range commands() {
		// Match whole words, so a single argument "spec lint x" is a usage error, not a match.
		words := strings.Fields(c.name)
		if len(args) >= len(words) && slices.Equal(args[:len(words)], words) {
			err := c.run(ctx, args[len(words):])
			switch {
			case err == nil:
				return exitOK
			case errors.Is(err, errGate):
				fmt.Fprintln(os.Stderr, "argus:", err)
				return exitGate
			case errors.Is(err, errDep):
				fmt.Fprintln(os.Stderr, "argus:", err)
				return exitDepend
			case errors.Is(err, flag.ErrHelp):
				return exitOK
			default:
				fmt.Fprintf(os.Stderr, "argus: %v\nusage: %s\n", err, c.usage)
				return exitUsage
			}
		}
	}
	fmt.Fprintln(os.Stderr, "usage:")
	for _, c := range commands() {
		fmt.Fprintln(os.Stderr, "  "+c.usage)
	}
	fmt.Fprintln(os.Stderr, "  argus version")
	return exitUsage
}

// parse handles "<positional> --flags" in any order, which stdlib flag does not.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}
