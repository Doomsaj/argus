package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEffectiveMode(t *testing.T) {
	g := Gate{Mode: "warn", WarnUntil: Date{time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}}
	for _, c := range []struct {
		now  time.Time
		want string
	}{
		{time.Date(2026, 12, 1, 23, 59, 0, 0, time.UTC), "warn"},
		{time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC), "block"},
	} {
		if got := g.EffectiveMode(c.now); got != c.want {
			t.Errorf("%s: got %s want %s", c.now, got, c.want)
		}
	}
}

func TestLoadCollectsAllErrors(t *testing.T) {
	p := filepath.Join(t.TempDir(), "argus.yaml")
	os.WriteFile(p, []byte(`service: Bad_Name
group: catalog
role: [provider]
spec: {path: x.json, generator: nope}
gate: {mode: warn}
waivers:
  - {rule: r, path: p, reason: x, approvedBy: catalog, expires: 2027-12-01}
`), 0o644)
	_, err := Load(p, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"service:", "spec.generator", "gate.warnUntil", "own waiver", "expires: more than"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestLoadUnknownKey(t *testing.T) {
	p := filepath.Join(t.TempDir(), "argus.yaml")
	os.WriteFile(p, []byte("service: a-b\ngroup: c-d\nrole: [consumer]\ngate: {mode: block}\nwaiver: []\n"), 0o644)
	if _, err := Load(p, time.Now()); err == nil || !strings.Contains(err.Error(), "waiver") {
		t.Fatalf("want unknown key error, got %v", err)
	}
}

func TestEnvGitHub(t *testing.T) {
	ev := filepath.Join(t.TempDir(), "event.json")
	os.WriteFile(ev, []byte(`{"pull_request":{"number":42}}`), 0o644)
	vars := map[string]string{
		"GITHUB_ACTIONS": "true", "GITHUB_SHA": "0123456789abcdef0123456789abcdef01234567",
		"GITHUB_REF_NAME": "42/merge", "GITHUB_HEAD_REF": "feature", "GITHUB_RUN_ID": "7",
		"GITHUB_REPOSITORY": "me/demo", "GITHUB_EVENT_PATH": ev, "CI_COMMIT_SHA": "ignored",
	}
	e := envFrom(func(k string) string { return vars[k] })
	short, _ := e.ShortSHA()
	if e.CI != "github" || e.RefName != "feature" || e.PRNumber != "42" || e.PipelineID != "7" ||
		short != "0123456789ab" || e.GitHubAPI != "https://api.github.com" || e.PipelineLabel() != "github.run" {
		t.Fatalf("got %+v", e)
	}
}

func TestEnvGitLab(t *testing.T) {
	vars := map[string]string{"GITLAB_CI": "true", "CI_COMMIT_SHA": "abc", "CI_MERGE_REQUEST_IID": "3"}
	e := envFrom(func(k string) string { return vars[k] })
	if e.CI != "gitlab" || e.MRIID != "3" || e.PipelineLabel() != "gitlab.pipeline" {
		t.Fatalf("got %+v", e)
	}
	if _, err := e.ShortSHA(); err == nil {
		t.Fatal("short SHA must fail")
	}
}

func TestTeams(t *testing.T) {
	m, err := Teams()
	if err != nil || m["argus-platform"].GitLabGroup != "argus" {
		t.Fatalf("got %v %v", m, err)
	}
}
