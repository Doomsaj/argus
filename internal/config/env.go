package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// Env is everything argus reads from the environment. CI predefined variables (GitLab CI_*
// or GitHub Actions GITHUB_*) are read here too, so commands never call os.Getenv directly
// (tests inject Env).
type Env struct {
	RegistryURL, OIDCTokenURL, OIDCClientID, OIDCClientSecret string
	BrokerURL, BrokerUser, BrokerPassword, PactBrokerCLI      string
	GitLabToken, GitLabAPI, ProjectID, MRIID                  string
	CommitSHA, RefName, PipelineID, BreakGlass                string

	// CI is "github" when GITHUB_ACTIONS=true, "gitlab" when GITLAB_CI=true, "" otherwise
	// (local run: CI_COMMIT_SHA etc. may still be set by hand).
	CI string
	// GitHub Actions only.
	GitHubToken, GitHubAPI, Repository, PRNumber, StepSummary string
}

func FromEnv() Env { return envFrom(os.Getenv) }

func envFrom(g func(string) string) Env {
	e := Env{
		RegistryURL: g("ARGUS_REGISTRY_URL"), OIDCTokenURL: g("ARGUS_OIDC_TOKEN_URL"),
		OIDCClientID: g("ARGUS_OIDC_CLIENT_ID"), OIDCClientSecret: g("ARGUS_OIDC_CLIENT_SECRET"),
		BrokerURL: g("PACT_BROKER_BASE_URL"), BrokerUser: g("PACT_BROKER_USERNAME"), BrokerPassword: g("PACT_BROKER_PASSWORD"),
		PactBrokerCLI: g("ARGUS_PACT_BROKER_CLI"),
		GitLabToken:   g("ARGUS_GITLAB_TOKEN"), GitLabAPI: g("CI_API_V4_URL"), ProjectID: g("CI_PROJECT_ID"),
		MRIID: g("CI_MERGE_REQUEST_IID"), CommitSHA: g("CI_COMMIT_SHA"), RefName: g("CI_COMMIT_REF_NAME"),
		PipelineID: g("CI_PIPELINE_ID"), BreakGlass: g("ARGUS_BREAK_GLASS"),
	}
	if g("GITLAB_CI") == "true" {
		e.CI = "gitlab"
	}
	if g("GITHUB_ACTIONS") == "true" {
		e.CI = "github"
		e.CommitSHA, e.PipelineID, e.Repository = g("GITHUB_SHA"), g("GITHUB_RUN_ID"), g("GITHUB_REPOSITORY")
		// Pull request events: GITHUB_REF_NAME is "<n>/merge", the source branch is GITHUB_HEAD_REF.
		e.RefName = g("GITHUB_HEAD_REF")
		if e.RefName == "" {
			e.RefName = g("GITHUB_REF_NAME")
		}
		// The GitHub actions export ARGUS_GITHUB_TOKEN; GITHUB_TOKEN covers direct use.
		e.GitHubToken, e.StepSummary = g("ARGUS_GITHUB_TOKEN"), g("GITHUB_STEP_SUMMARY")
		if e.GitHubToken == "" {
			e.GitHubToken = g("GITHUB_TOKEN")
		}
		e.GitHubAPI = g("GITHUB_API_URL")
		if e.GitHubAPI == "" {
			e.GitHubAPI = "https://api.github.com"
		}
		e.PRNumber = prNumber(g("GITHUB_EVENT_PATH"))
	}
	return e
}

// prNumber reads the pull request number from the Actions event payload ("" if not a PR event).
func prNumber(eventPath string) string {
	if eventPath == "" {
		return ""
	}
	raw, err := os.ReadFile(eventPath)
	if err != nil {
		return ""
	}
	var ev struct {
		PullRequest struct {
			Number int `json:"number"`
		} `json:"pull_request"`
	}
	if json.Unmarshal(raw, &ev) != nil || ev.PullRequest.Number == 0 {
		return ""
	}
	return strconv.Itoa(ev.PullRequest.Number)
}

// ShortSHA is the Apicurio version key: first 12 chars of the commit SHA.
// In merged-results pipelines CI_COMMIT_SHA is the merge commit, used consistently everywhere.
func (e Env) ShortSHA() (string, error) {
	if len(e.CommitSHA) < 12 {
		return "", fmt.Errorf("commit SHA %q (CI_COMMIT_SHA / GITHUB_SHA): need a full commit SHA", e.CommitSHA)
	}
	return e.CommitSHA[:12], nil
}

// PipelineLabel is the Apicurio label key for the CI run id.
func (e Env) PipelineLabel() string {
	if e.CI == "github" {
		return "github.run"
	}
	return "gitlab.pipeline"
}

// Require returns an error naming every missing variable.
func Require(pairs ...string) error {
	var missing []string
	for i := 0; i+1 < len(pairs); i += 2 {
		if pairs[i+1] == "" {
			missing = append(missing, pairs[i])
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing environment variables: %v", missing)
	}
	return nil
}
