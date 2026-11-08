package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.digikala.com/argus/argus/internal/config"
)

const (
	sha  = "0123456789abcdef0123456789abcdef01234567"
	base = "../../testdata/check/base.yaml"
)

func init() { now = func() time.Time { return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) } }

// setup writes argus.yaml (mode warn|block) and clears CI variables so the host env cannot leak in.
func setup(t *testing.T, mode, role string) string {
	t.Helper()
	for _, k := range []string{"GITLAB_CI", "GITHUB_ACTIONS", "ARGUS_GITLAB_TOKEN", "GITHUB_TOKEN", "GITHUB_STEP_SUMMARY",
		"CI_MERGE_REQUEST_IID", "ARGUS_BREAK_GLASS", "ARGUS_OIDC_TOKEN_URL", "PACT_BROKER_BASE_URL", "ARGUS_REGISTRY_URL"} {
		t.Setenv(k, "")
	}
	t.Setenv("CI_COMMIT_SHA", sha)
	dir := t.TempDir()
	cfg := fmt.Sprintf(`service: product-detail
group: catalog
role: [%s]
spec: {path: x.yaml, generator: springdoc}
gate: {mode: %s, warnUntil: 2026-11-30}
waivers:
  - rule: response-property-type-generalized
    path: "POST /v1/products/ 200 /items/selling_price"
    reason: coordinated migration
    approvedBy: shopping-discovery
    expires: 2026-11-20
`, role, mode)
	p := filepath.Join(dir, "argus.yaml")
	if err := os.WriteFile(p, []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func revision(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(base)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "revision.yaml")
	rev := strings.Replace(string(b), "selling_price: {type: integer, format: int64}", "selling_price: {type: number, format: double}", 1)
	if err := os.WriteFile(p, []byte(rev), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// fakePactBroker installs a script as ARGUS_PACT_BROKER_CLI that logs its args and exits with code.
func fakePactBroker(t *testing.T, code int) (log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "args.log")
	script := filepath.Join(dir, "pact-broker")
	os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %s\nexit %d\n", log, code)), 0o755)
	t.Setenv("ARGUS_PACT_BROKER_CLI", script)
	t.Setenv("PACT_BROKER_BASE_URL", "http://broker.invalid")
	return log
}

func TestSpecCheckExitCodes(t *testing.T) {
	for _, c := range []struct {
		mode string
		want int
	}{{"block", exitGate}, {"warn", exitOK}} {
		cfg := setup(t, c.mode, "provider")
		md := filepath.Join(t.TempDir(), "r.md")
		got := dispatch(t.Context(), []string{"spec", "check", revision(t), "--config", cfg, "--base", base,
			"--md", md, "--junit", md + ".xml"})
		if got != c.want {
			t.Errorf("%s: exit %d want %d", c.mode, got, c.want)
		}
		b, _ := os.ReadFile(md)
		if !strings.Contains(string(b), "UNAPPROVED (shopping-discovery") {
			t.Errorf("%s: report:\n%s", c.mode, b)
		}
	}
}

func TestUsageErrors(t *testing.T) {
	cfg := setup(t, "block", "provider")
	for _, args := range [][]string{
		{"spec", "lint"},
		{"spec", "lint", "missing.yaml", "--config", cfg},
		{"spec", "lint", base, "--config", "missing.yaml"},
		{"bogus"},
		{"can-i-deploy", "--config", cfg},
	} {
		if got := dispatch(t.Context(), args); got != exitUsage {
			t.Errorf("%v: exit %d want 2", args, got)
		}
	}
	if got := dispatch(t.Context(), []string{"version"}); got != exitOK {
		t.Errorf("version: %d", got)
	}
	if got := dispatch(t.Context(), []string{"spec", "lint", base, "--config", cfg}); got != exitOK {
		t.Errorf("lint clean base: %d", got)
	}
}

func TestVerifySamples(t *testing.T) {
	cfg := setup(t, "block", "provider")
	for dir, want := range map[string]int{"../../testdata/samples": exitGate, "../../testdata/samples-ok": exitOK, t.TempDir(): exitGate} {
		if got := dispatch(t.Context(), []string{"verify", "samples", "--spec", base, "--dir", dir, "--config", cfg}); got != want {
			t.Errorf("%s: exit %d want %d", dir, got, want)
		}
	}
}

func TestCanIDeployConsumer(t *testing.T) {
	cfg := setup(t, "block", "consumer")
	log := fakePactBroker(t, 0)
	if got := dispatch(t.Context(), []string{"can-i-deploy", "--to", "production", "--config", cfg}); got != exitOK {
		t.Fatalf("exit %d", got)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "can-i-deploy --pacticipant product-detail --version "+sha+" --to-environment production") {
		t.Fatalf("args: %s", b)
	}
	fakePactBroker(t, 1)
	if got := dispatch(t.Context(), []string{"can-i-deploy", "--to", "production", "--config", cfg}); got != exitGate {
		t.Errorf("matrix no: exit %d want 1", got)
	}
	t.Setenv("ARGUS_BREAK_GLASS", "INC-1: provider hotfix for outage")
	if got := dispatch(t.Context(), []string{"can-i-deploy", "--to", "production", "--config", cfg}); got != exitOK {
		t.Errorf("break-glass: exit %d want 0", got)
	}
	t.Setenv("ARGUS_BREAK_GLASS", "")
	t.Setenv("ARGUS_PACT_BROKER_CLI", "/nonexistent/pact-broker")
	if got := dispatch(t.Context(), []string{"can-i-deploy", "--to", "production", "--config", cfg}); got != exitDepend {
		t.Errorf("missing CLI: exit %d want 3", got)
	}
}

// fakeApicurio records calls; auth disabled (no ARGUS_OIDC_TOKEN_URL) must send no Authorization header.
func fakeApicurio(t *testing.T) (*httptest.Server, *[]string) {
	var mu sync.Mutex
	var calls []string
	branches := map[string][]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected Authorization header")
		}
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		const art = "/groups/catalog/artifacts/product-detail"
		switch {
		case r.Method == "POST" && r.URL.Path == "/groups/catalog/artifacts":
			var ca struct {
				FirstVersion struct {
					Version string            `json:"version"`
					Labels  map[string]string `json:"labels"`
				} `json:"firstVersion"`
			}
			json.Unmarshal(body, &ca)
			if ca.FirstVersion.Version != sha[:12] || ca.FirstVersion.Labels["git.sha"] != sha {
				t.Errorf("create body: %s", body)
			}
			fmt.Fprint(w, `{}`)
		case r.Method == "GET" && strings.HasPrefix(r.URL.Path, art+"/branches/"):
			b := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, art+"/branches/"), "/versions")
			if len(branches[b]) == 0 {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"versions":[{"version":%q}]}`, branches[b][len(branches[b])-1])
		case r.Method == "POST" && r.URL.Path == art+"/branches":
			var cb struct {
				BranchID string   `json:"branchId"`
				Versions []string `json:"versions"`
			}
			json.Unmarshal(body, &cb)
			branches[cb.BranchID] = cb.Versions
			fmt.Fprint(w, `{}`)
		case r.Method == "POST" && strings.HasPrefix(r.URL.Path, art+"/branches/"):
			http.NotFound(w, r) // branch does not exist yet in this fake
		case r.Method == "GET" && r.URL.Path == art+"/versions/"+sha[:12]:
			fmt.Fprint(w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("ARGUS_REGISTRY_URL", srv.URL)
	return srv, &calls
}

func TestPublishCanIDeployRecordLocalAuth(t *testing.T) {
	cfg := setup(t, "block", "provider")
	_, calls := fakeApicurio(t)
	log := fakePactBroker(t, 0)
	steps := [][]string{
		{"spec", "publish", base, "--branch", "main", "--config", cfg},
		{"spec", "publish", base, "--branch", "main", "--config", cfg}, // retry: tip already equals version
		{"can-i-deploy", "--to", "production", "--config", cfg},
		{"deploy", "record", "--env", "production", "--config", cfg},
	}
	for _, s := range steps {
		if got := dispatch(t.Context(), s); got != exitOK {
			t.Fatalf("%v: exit %d, calls %v", s, got, *calls)
		}
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{
		"POST /groups/catalog/artifacts?ifExists=FIND_OR_CREATE_VERSION",
		"POST /groups/catalog/artifacts/product-detail/branches",
		"GET /groups/catalog/artifacts/product-detail/versions/0123456789ab",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing call %q in\n%s", want, joined)
		}
	}
	if n := strings.Count(joined, "POST /groups/catalog/artifacts/product-detail/branches\n"); n != 2 { // main + production
		t.Errorf("branch creations: %d\n%s", n, joined)
	}
	b, _ := os.ReadFile(log)
	for _, want := range []string{"create-or-update-version --pacticipant product-detail --version " + sha,
		"record-deployment --pacticipant product-detail --version " + sha + " --environment production"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing pact-broker call %q in\n%s", want, b)
		}
	}
}

// GitHub: waiver approved by a listed login, step summary appended, PR comment upserted.
func TestSpecCheckGitHub(t *testing.T) {
	cfg := setup(t, "block", "provider")
	var mu sync.Mutex
	var posted []string
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer ghtok" {
			t.Errorf("auth header %q", r.Header.Get("Authorization"))
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /repos/me/demo/pulls/42/reviews":
			fmt.Fprint(w, `[{"state":"APPROVED","user":{"login":"Sd-Reviewer"}},{"state":"CHANGES_REQUESTED","user":{"login":"other"}}]`)
		case "GET /repos/me/demo/issues/42/comments":
			fmt.Fprint(w, `[{"id":7,"body":"<!-- argus:spec-check -->\nold"}]`)
		case "PATCH /repos/me/demo/issues/comments/7":
			posted = append(posted, "patch")
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	dir := t.TempDir()
	ev := filepath.Join(dir, "event.json")
	os.WriteFile(ev, []byte(`{"pull_request":{"number":42}}`), 0o644)
	summary := filepath.Join(dir, "summary.md")
	for k, v := range map[string]string{"GITHUB_ACTIONS": "true", "GITHUB_SHA": sha, "GITHUB_REPOSITORY": "me/demo",
		"GITHUB_EVENT_PATH": ev, "GITHUB_TOKEN": "ghtok", "GITHUB_API_URL": gh.URL, "GITHUB_STEP_SUMMARY": summary} {
		t.Setenv(k, v)
	}
	// The embedded teams.yaml ships without logins; swap in a test map.
	old := teamsFn
	teamsFn = func() (map[string]config.Team, error) {
		return map[string]config.Team{"shopping-discovery": {GitHubUsers: []string{"sd-reviewer"}}}, nil
	}
	defer func() { teamsFn = old }()

	md := filepath.Join(dir, "r.md")
	if got := dispatch(t.Context(), []string{"spec", "check", revision(t), "--config", cfg, "--base", base, "--md", md, "--junit", md + ".xml"}); got != exitOK {
		t.Fatalf("approved waiver: exit %d", got)
	}
	s, _ := os.ReadFile(summary)
	if !strings.Contains(string(s), "APPLIED (shopping-discovery") || len(posted) != 1 {
		t.Fatalf("summary:\n%s\nposted %v", s, posted)
	}
}

func TestDispatchSingleArgWithSpaces(t *testing.T) {
	if got := dispatch(t.Context(), []string{"spec lint x"}); got != exitUsage {
		t.Fatalf("exit = %d, want %d", got, exitUsage)
	}
}
