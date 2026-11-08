// Package pactbroker wraps the pact-broker CLI for writes and gate decisions, and calls the
// Broker HTTP API directly for the read-only consumer lookup.
package pactbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// CLI runs `pact-broker` (pact_broker-client, bundled in the argus image).
// It reads PACT_BROKER_BASE_URL / PACT_BROKER_USERNAME / PACT_BROKER_PASSWORD itself.
type CLI struct {
	Bin    string // ARGUS_PACT_BROKER_CLI, default "pact-broker" on PATH
	Stdout io.Writer
}

func (c CLI) run(ctx context.Context, args ...string) error {
	bin := c.Bin
	if bin == "" {
		bin = "pact-broker"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = c.Stdout, os.Stderr
	err := cmd.Run() // *exec.ExitError: non-zero exit, e.g. can-i-deploy said no
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		return fmt.Errorf("%w: %v", ErrNotRun, err)
	}
	return err
}

// ErrNotRun means the pact-broker binary could not be started (missing from PATH, not executable):
// a dependency problem, not a gate verdict.
var ErrNotRun = errors.New("pact-broker CLI not runnable (set ARGUS_PACT_BROKER_CLI or put pact-broker on PATH)")

// CanIDeploy returns nil when the matrix allows the deploy. retry-while-unknown covers
// verification results that are still being published by a webhook-triggered pipeline.
func (c CLI) CanIDeploy(ctx context.Context, pacticipant, version, env string) error {
	return c.run(ctx, "can-i-deploy", "--pacticipant", pacticipant, "--version", version,
		"--to-environment", env, "--retry-while-unknown", "12", "--retry-interval", "10", "--output", "table")
}

func (c CLI) RecordDeployment(ctx context.Context, pacticipant, version, env string) error {
	return c.run(ctx, "record-deployment", "--pacticipant", pacticipant, "--version", version, "--environment", env)
}

// CreateVersion registers a provider version with its branch even when it has no pacts yet,
// so can-i-deploy does not answer "unknown version" for a provider without consumers.
func (c CLI) CreateVersion(ctx context.Context, pacticipant, version, branch string) error {
	return c.run(ctx, "create-or-update-version", "--pacticipant", pacticipant, "--version", version, "--branch", branch)
}

// Consumer is one consumer of a provider and the endpoints its pacts exercise.
type Consumer struct {
	Name      string   `json:"name"`
	Endpoints []string `json:"endpoints"` // "POST /v1/products/" as written in the pact (concrete paths)
}

type HTTP struct {
	BaseURL, User, Password string
	Client                  *http.Client
}

func NewHTTP(base, user, pass string) *HTTP {
	return &HTTP{BaseURL: strings.TrimRight(base, "/"), User: user, Password: pass, Client: &http.Client{Timeout: 10 * time.Second}}
}

func (h *HTTP) do(ctx context.Context, method, u string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.SetBasicAuth(h.User, h.Password)
	req.Header.Set("Accept", "application/hal+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := h.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("broker %s %s: HTTP %d", method, u, resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Consumers asks the broker which pacts the provider must verify (main branch + deployed
// consumer versions), then reads each pact's interactions. Same selectors the providers use.
func (h *HTTP) Consumers(ctx context.Context, provider string) ([]Consumer, error) {
	var fv struct {
		Embedded struct {
			Pacts []struct {
				Links struct {
					Self struct {
						Href string `json:"href"`
					} `json:"self"`
				} `json:"_links"`
			} `json:"pacts"`
		} `json:"_embedded"`
	}
	sel := map[string]any{"consumerVersionSelectors": []map[string]bool{{"mainBranch": true}, {"deployedOrReleased": true}}}
	u := h.BaseURL + "/pacts/provider/" + url.PathEscape(provider) + "/for-verification"
	if err := h.do(ctx, http.MethodPost, u, sel, &fv); err != nil {
		return nil, err
	}
	byName := map[string]map[string]bool{}
	for _, p := range fv.Embedded.Pacts {
		var pact struct {
			Consumer struct {
				Name string `json:"name"`
			} `json:"consumer"`
			Interactions []struct {
				Request struct {
					Method string `json:"method"`
					Path   string `json:"path"`
				} `json:"request"`
			} `json:"interactions"`
		}
		if err := h.do(ctx, http.MethodGet, p.Links.Self.Href, nil, &pact); err != nil {
			return nil, err
		}
		if byName[pact.Consumer.Name] == nil {
			byName[pact.Consumer.Name] = map[string]bool{}
		}
		for _, i := range pact.Interactions {
			if i.Request.Method != "" { // V4 message interactions have no HTTP request
				byName[pact.Consumer.Name][strings.ToUpper(i.Request.Method)+" "+i.Request.Path] = true
			}
		}
	}
	var out []Consumer
	for name, eps := range byName {
		c := Consumer{Name: name}
		for e := range eps {
			c.Endpoints = append(c.Endpoints, e)
		}
		slices.Sort(c.Endpoints)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Consumer) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// Affected returns consumers whose pacts call method+template ("/v1/products/{id}").
func Affected(cs []Consumer, method, template string) []string {
	var out []string
	for _, c := range cs {
		for _, e := range c.Endpoints {
			m, p, _ := strings.Cut(e, " ")
			if m == method && matchTemplate(template, p) {
				out = append(out, c.Name)
				break
			}
		}
	}
	return out
}

func matchTemplate(tmpl, path string) bool {
	if q := strings.IndexByte(path, '?'); q >= 0 {
		path = path[:q]
	}
	ts, ps := strings.Split(tmpl, "/"), strings.Split(path, "/")
	if len(ts) != len(ps) {
		return false
	}
	for i := range ts {
		if strings.HasPrefix(ts[i], "{") && strings.HasSuffix(ts[i], "}") && ps[i] != "" {
			continue
		}
		if ts[i] != ps[i] {
			return false
		}
	}
	return true
}

// Pacticipants lists every pacticipant name (GET /pacticipants).
func (h *HTTP) Pacticipants(ctx context.Context) ([]string, error) {
	var res struct {
		Embedded struct {
			Pacticipants []struct {
				Name string `json:"name"`
			} `json:"pacticipants"`
		} `json:"_embedded"`
	}
	if err := h.do(ctx, http.MethodGet, h.BaseURL+"/pacticipants", nil, &res); err != nil {
		return nil, err
	}
	var out []string
	for _, p := range res.Embedded.Pacticipants {
		out = append(out, p.Name)
	}
	slices.Sort(out)
	return out, nil
}

// CurrentlyDeployed maps pacticipant -> full version SHAs currently deployed to env
// (GET /environments, then /environments/{uuid}/deployed-versions/currently-deployed).
func (h *HTTP) CurrentlyDeployed(ctx context.Context, env string) (map[string][]string, error) {
	var envs struct {
		Embedded struct {
			Environments []struct {
				UUID string `json:"uuid"`
				Name string `json:"name"`
			} `json:"environments"`
		} `json:"_embedded"`
	}
	if err := h.do(ctx, http.MethodGet, h.BaseURL+"/environments", nil, &envs); err != nil {
		return nil, err
	}
	for _, e := range envs.Embedded.Environments {
		if e.Name != env {
			continue
		}
		var dep struct {
			Embedded struct {
				DeployedVersions []struct {
					Embedded struct {
						Pacticipant struct {
							Name string `json:"name"`
						} `json:"pacticipant"`
						Version struct {
							Number string `json:"number"`
						} `json:"version"`
					} `json:"_embedded"`
				} `json:"deployedVersions"`
			} `json:"_embedded"`
		}
		u := h.BaseURL + "/environments/" + url.PathEscape(e.UUID) + "/deployed-versions/currently-deployed"
		if err := h.do(ctx, http.MethodGet, u, nil, &dep); err != nil {
			return nil, err
		}
		out := map[string][]string{}
		for _, d := range dep.Embedded.DeployedVersions {
			n := d.Embedded.Pacticipant.Name
			out[n] = append(out[n], d.Embedded.Version.Number)
		}
		return out, nil
	}
	return nil, fmt.Errorf("broker environment %q not found", env)
}
