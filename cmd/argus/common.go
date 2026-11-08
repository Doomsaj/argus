package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/getkin/kin-openapi/openapi3"

	"git.digikala.com/argus/argus/internal/config"
	"git.digikala.com/argus/argus/internal/pactbroker"
	"git.digikala.com/argus/argus/internal/registry"
)

var now = time.Now // replaced in tests

func loadConfig(path string) (*config.Config, error) {
	return config.Load(path, now())
}

func loadSpec(ctx context.Context, path string) (*openapi3.T, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	doc, err := loader.LoadFromData(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := doc.Validate(ctx); err != nil {
		return nil, nil, fmt.Errorf("%s: invalid OpenAPI: %w", path, err)
	}
	return doc, raw, nil
}

func registryClient(env config.Env) (*registry.Client, error) {
	if err := config.Require("ARGUS_REGISTRY_URL", env.RegistryURL); err != nil {
		return nil, err
	}
	if env.OIDCTokenURL == "" {
		c := registry.New(env.RegistryURL, nil) // local Apicurio: auth disabled, or basic auth at a proxy
		c.User, c.Password = env.RegistryUser, env.RegistryPassword
		return c, nil
	}
	if err := config.Require("ARGUS_OIDC_CLIENT_ID", env.OIDCClientID, "ARGUS_OIDC_CLIENT_SECRET", env.OIDCClientSecret); err != nil {
		return nil, err
	}
	ts := &registry.TokenSource{TokenURL: env.OIDCTokenURL, ClientID: env.OIDCClientID, ClientSecret: env.OIDCClientSecret,
		HTTP: registry.New("", nil).HTTP}
	return registry.New(env.RegistryURL, ts), nil
}

func brokerCLI(env config.Env) pactbroker.CLI {
	return pactbroker.CLI{Bin: env.PactBrokerCLI, Stdout: os.Stdout}
}

// soft turns a dependency error into a warning in warn mode (fail-open while onboarding),
// and into errDep in block mode (fail-closed).
func soft(effectiveMode string, err error) error {
	if err == nil {
		return nil
	}
	if effectiveMode == "block" {
		return fmt.Errorf("%w: %v", errDep, err)
	}
	fmt.Fprintf(os.Stderr, "argus: WARNING (warn mode, not failing): %v\n", err)
	return nil
}
