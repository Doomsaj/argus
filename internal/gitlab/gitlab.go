// Package gitlab posts the argus MR note and checks waiver approvals (GitLab REST v4).
package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.digikala.com/argus/argus/internal/config"
)

// Marker identifies the single argus note per MR so reruns edit instead of spamming.
const Marker = "<!-- argus:spec-check -->"

type Client struct {
	API, Token, Project string // CI_API_V4_URL, ARGUS_GITLAB_TOKEN, CI_PROJECT_ID
	HTTP                *http.Client
}

func New(api, token, project string) *Client {
	return &Client{API: strings.TrimRight(api, "/"), Token: token, Project: project, HTTP: &http.Client{Timeout: 10 * time.Second}}
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) (http.Header, error) {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.API+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("gitlab %s %s: HTTP %d: %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		return resp.Header, json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.Header, nil
}

func (c *Client) project() string { return "/projects/" + url.PathEscape(c.Project) }

type note struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

// UpsertMRNote edits the note that contains Marker, or creates it.
func (c *Client) UpsertMRNote(ctx context.Context, mrIID, body string) error {
	body = Marker + "\n" + body
	base := c.project() + "/merge_requests/" + url.PathEscape(mrIID) + "/notes"
	for page := "1"; page != ""; {
		var notes []note
		h, err := c.do(ctx, http.MethodGet, base+"?per_page=100&sort=asc&page="+page, nil, &notes)
		if err != nil {
			return err
		}
		for _, n := range notes {
			if strings.HasPrefix(n.Body, Marker) {
				_, err := c.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d", base, n.ID), map[string]string{"body": body}, nil)
				return err
			}
		}
		page = h.Get("X-Next-Page")
	}
	_, err := c.do(ctx, http.MethodPost, base, map[string]string{"body": body}, nil)
	return err
}

// CommitNote writes an audit comment on a commit (break-glass trail).
func (c *Client) CommitNote(ctx context.Context, sha, text string) error {
	_, err := c.do(ctx, http.MethodPost, c.project()+"/repository/commits/"+sha+"/comments", map[string]string{"note": text}, nil)
	return err
}

// Approver returns a function that tells whether anyone in team's GitLab group approved
// the MR that carries this commit. teams maps argus team slugs to their GitLab group paths.
// On MR pipelines mrIID is set; on default-branch pipelines the MR is looked up from the SHA.
func (c *Client) Approver(ctx context.Context, mrIID, sha string, teams map[string]config.Team) (func(team string) (bool, error), error) {
	if mrIID == "" {
		var mrs []struct {
			IID   int    `json:"iid"`
			State string `json:"state"`
		}
		if _, err := c.do(ctx, http.MethodGet, c.project()+"/repository/commits/"+sha+"/merge_requests", nil, &mrs); err != nil {
			return nil, err
		}
		for _, m := range mrs {
			if m.State == "merged" {
				mrIID = fmt.Sprint(m.IID)
			}
		}
		if mrIID == "" {
			return func(string) (bool, error) { return false, nil }, nil // direct push: no approval possible
		}
	}
	var appr struct {
		ApprovedBy []struct {
			User struct {
				ID int `json:"id"`
			} `json:"user"`
		} `json:"approved_by"`
	}
	if _, err := c.do(ctx, http.MethodGet, c.project()+"/merge_requests/"+mrIID+"/approvals", nil, &appr); err != nil {
		return nil, err
	}
	cache := map[string]bool{}
	return func(team string) (bool, error) {
		if v, ok := cache[team]; ok {
			return v, nil
		}
		t, ok := teams[team]
		group := t.GitLabGroup
		if !ok || group == "" {
			return false, fmt.Errorf("approvedBy %q: unknown team (platform teams map)", team)
		}
		for _, a := range appr.ApprovedBy {
			// /members/all includes inherited members; 404 means not a member.
			_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/groups/%s/members/all/%d", url.PathEscape(group), a.User.ID), nil, nil)
			if err == nil {
				cache[team] = true
				return true, nil
			}
			if !strings.Contains(err.Error(), "HTTP 404") {
				return false, err
			}
		}
		cache[team] = false
		return false, nil
	}, nil
}
