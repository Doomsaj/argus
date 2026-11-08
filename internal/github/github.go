// Package github mirrors internal/gitlab for GitHub Actions: the argus PR comment, the
// break-glass commit comment and waiver approvals from PR reviews (GitHub REST v3).
package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"git.digikala.com/argus/argus/internal/config"
)

// Marker identifies the single argus comment per PR so reruns edit instead of spamming.
const Marker = "<!-- argus:spec-check -->"

type Client struct {
	API, Token, Repo string // GITHUB_API_URL, GITHUB_TOKEN, GITHUB_REPOSITORY (owner/name)
	HTTP             *http.Client
}

func New(api, token, repo string) *Client {
	return &Client{API: strings.TrimRight(api, "/"), Token: token, Repo: repo, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

var nextLink = regexp.MustCompile(`<([^>]+)>;\s*rel="next"`)

// do calls path (relative to API, or an absolute pagination URL) and returns the next page URL.
func (c *Client) do(ctx context.Context, method, path string, in, out any) (next string, err error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	u := path
	if !strings.HasPrefix(u, "http") {
		u = c.API + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("github %s %s: HTTP %d: %s", method, path, resp.StatusCode, b)
	}
	if m := nextLink.FindStringSubmatch(resp.Header.Get("Link")); m != nil {
		next = m[1]
	}
	if out != nil {
		return next, json.NewDecoder(resp.Body).Decode(out)
	}
	return next, nil
}

func (c *Client) repo() string { return "/repos/" + c.Repo }

// UpsertPRComment edits the comment that starts with Marker, or creates it.
func (c *Client) UpsertPRComment(ctx context.Context, pr, body string) error {
	body = Marker + "\n" + body
	base := c.repo() + "/issues/" + pr + "/comments"
	for page := base + "?per_page=100"; page != ""; {
		var comments []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		next, err := c.do(ctx, http.MethodGet, page, nil, &comments)
		if err != nil {
			return err
		}
		for _, cm := range comments {
			if strings.HasPrefix(cm.Body, Marker) {
				_, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("%s/issues/comments/%d", c.repo(), cm.ID), map[string]string{"body": body}, nil)
				return err
			}
		}
		page = next
	}
	_, err := c.do(ctx, http.MethodPost, base, map[string]string{"body": body}, nil)
	return err
}

// CommitComment writes an audit comment on a commit (break-glass trail).
func (c *Client) CommitComment(ctx context.Context, sha, text string) error {
	_, err := c.do(ctx, http.MethodPost, c.repo()+"/commits/"+sha+"/comments", map[string]string{"body": text}, nil)
	return err
}

// Approver returns a function that tells whether a GitHub login listed under the team's
// githubUsers in teams.yaml has an APPROVED review on the PR. On push pipelines (no pr)
// the merged PR is looked up from the commit SHA.
func (c *Client) Approver(ctx context.Context, pr, sha string, teams map[string]config.Team) (func(team string) (bool, error), error) {
	if pr == "" {
		var prs []struct {
			Number   int     `json:"number"`
			MergedAt *string `json:"merged_at"`
		}
		if _, err := c.do(ctx, http.MethodGet, c.repo()+"/commits/"+sha+"/pulls", nil, &prs); err != nil {
			return nil, err
		}
		for _, p := range prs {
			if p.MergedAt != nil {
				pr = fmt.Sprint(p.Number)
			}
		}
		if pr == "" {
			return func(string) (bool, error) { return false, nil }, nil // direct push: no approval possible
		}
	}
	// A user's latest APPROVED / CHANGES_REQUESTED / DISMISSED review is their current verdict.
	state := map[string]string{}
	for page := c.repo() + "/pulls/" + pr + "/reviews?per_page=100"; page != ""; {
		var reviews []struct {
			State string `json:"state"`
			User  struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		next, err := c.do(ctx, http.MethodGet, page, nil, &reviews)
		if err != nil {
			return nil, err
		}
		for _, r := range reviews { // chronological order
			if r.State != "COMMENTED" && r.State != "PENDING" {
				state[strings.ToLower(r.User.Login)] = r.State
			}
		}
		page = next
	}
	return func(team string) (bool, error) {
		t, ok := teams[team]
		if !ok {
			return false, fmt.Errorf("approvedBy %q: unknown team (platform teams map)", team)
		}
		for login, s := range state {
			if s == "APPROVED" && slices.ContainsFunc(t.GitHubUsers, func(u string) bool { return strings.EqualFold(u, login) }) {
				return true, nil
			}
		}
		return false, nil
	}, nil
}

// AppendStepSummary appends markdown to $GITHUB_STEP_SUMMARY (the job summary page).
func AppendStepSummary(path, md string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	_, err = f.WriteString(md + "\n")
	return errors.Join(err, f.Close())
}
