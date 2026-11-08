// Package registry is a minimal Apicurio Registry 3.x REST v3 client:
// only the calls argus needs (verify: spike S3 against 3.3.x).
// API: https://www.apicur.io/registry/docs/apicurio-registry/3.0.x/assets-attachments/registry-rest-api.htm
package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ErrNotFound is returned for 404s so callers can fall back (production -> main).
var ErrNotFound = errors.New("not found")

type Client struct {
	BaseURL string // ARGUS_REGISTRY_URL, e.g. https://registry.argus.digikala.internal/apis/registry/v3
	HTTP    *http.Client
	Token   *TokenSource
	// User/Password: basic auth when no OIDC is configured (Apicurio behind an authenticating proxy).
	User, Password string
}

func New(baseURL string, ts *TokenSource) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/"), HTTP: &http.Client{Timeout: 10 * time.Second}, Token: ts}
}

// TokenSource does the OIDC client-credentials grant and caches the token until 30s before expiry.
type TokenSource struct {
	TokenURL, ClientID, ClientSecret string
	HTTP                             *http.Client

	mu  sync.Mutex
	tok string
	exp time.Time
}

func (t *TokenSource) Get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.tok != "" && time.Now().Before(t.exp) {
		return t.tok, nil
	}
	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(t.ClientID), url.QueryEscape(t.ClientSecret)) // RFC 6749 2.3.1
	resp, err := t.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc token: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("oidc token: HTTP %d: %s", resp.StatusCode, b) // body never contains the secret
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", fmt.Errorf("oidc token: %w", err)
	}
	t.tok, t.exp = body.AccessToken, time.Now().Add(time.Duration(body.ExpiresIn-30)*time.Second)
	return t.tok, nil
}

func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return err
	}
	if c.Token != nil { // nil: registry with auth disabled (local dev, ARGUS_OIDC_TOKEN_URL empty)
		tok, err := c.Token.Get(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	} else if c.User != "" {
		req.SetBasicAuth(c.User, c.Password)
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%s %s: %w", method, path, ErrNotFound)
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, b)
	case out == nil:
		return nil
	case isRaw(out):
		_, err := io.Copy(out.(*bytes.Buffer), resp.Body)
		return err
	default:
		return json.NewDecoder(resp.Body).Decode(out)
	}
}

func isRaw(v any) bool { _, ok := v.(*bytes.Buffer); return ok }

func artifactPath(group, artifact string) string {
	return "/groups/" + url.PathEscape(group) + "/artifacts/" + url.PathEscape(artifact)
}

// BranchTip returns the newest version on a branch and its content.
// listBranchVersions is ordered from the tip to the oldest.
func (c *Client) BranchTip(ctx context.Context, group, artifact, branch string) (version string, content []byte, err error) {
	var res struct {
		Versions []struct {
			Version string `json:"version"`
		} `json:"versions"`
	}
	p := artifactPath(group, artifact) + "/branches/" + url.PathEscape(branch) + "/versions?limit=1"
	if err := c.do(ctx, http.MethodGet, p, nil, &res); err != nil {
		return "", nil, err
	}
	if len(res.Versions) == 0 {
		return "", nil, fmt.Errorf("branch %s is empty: %w", branch, ErrNotFound)
	}
	version = res.Versions[0].Version
	var buf bytes.Buffer
	err = c.do(ctx, http.MethodGet, artifactPath(group, artifact)+"/versions/"+url.PathEscape(version)+"/content", nil, &buf)
	return version, buf.Bytes(), err
}

// VersionExists is used by can-i-deploy and deploy record.
func (c *Client) VersionExists(ctx context.Context, group, artifact, version string) (bool, error) {
	err := c.do(ctx, http.MethodGet, artifactPath(group, artifact)+"/versions/"+url.PathEscape(version), nil, nil)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

type versionContent struct {
	Content     string `json:"content"`
	ContentType string `json:"contentType"`
}

type createVersion struct {
	Version string            `json:"version"`
	Content versionContent    `json:"content"`
	Labels  map[string]string `json:"labels,omitempty"`
}

type createArtifact struct {
	ArtifactID   string        `json:"artifactId"`
	ArtifactType string        `json:"artifactType"`
	FirstVersion createVersion `json:"firstVersion"`
}

// CreateVersion creates the artifact on first use and the version otherwise.
// FIND_OR_CREATE_VERSION makes pipeline retries idempotent: same SHA + same content = no-op.
// A 409 here means the VALIDITY rule rejected the spec or the SHA exists with other content.
func (c *Client) CreateVersion(ctx context.Context, group, artifact, version string, spec []byte, labels map[string]string, dryRun bool) error {
	q := url.Values{"ifExists": {"FIND_OR_CREATE_VERSION"}}
	if dryRun {
		q.Set("dryRun", "true")
	}
	ct := "application/x-yaml"
	if t := bytes.TrimSpace(spec); len(t) > 0 && t[0] == '{' {
		ct = "application/json"
	}
	body := createArtifact{
		ArtifactID:   artifact,
		ArtifactType: "OPENAPI",
		FirstVersion: createVersion{
			Version: version,
			Content: versionContent{Content: string(spec), ContentType: ct},
			Labels:  labels,
		},
	}
	return c.do(ctx, http.MethodPost, "/groups/"+url.PathEscape(group)+"/artifacts?"+q.Encode(), body, nil)
}

// AddToBranch appends version to branch, creating the branch on first use.
// Branches are append-only history: the tip is "what is live" for production/staging.
func (c *Client) AddToBranch(ctx context.Context, group, artifact, branch, version string) error {
	if tip, _, err := c.BranchTip(ctx, group, artifact, branch); err == nil && tip == version {
		return nil // retry of the same deploy job: already the tip
	}
	base := artifactPath(group, artifact) + "/branches"
	err := c.do(ctx, http.MethodPost, base+"/"+url.PathEscape(branch)+"/versions", map[string]string{"version": version}, nil)
	if errors.Is(err, ErrNotFound) {
		return c.do(ctx, http.MethodPost, base, map[string]any{"branchId": branch, "versions": []string{version}}, nil)
	}
	return err
}

// Artifact identifies one spec in the registry.
type Artifact struct {
	GroupID    string `json:"groupId"`
	ArtifactID string `json:"artifactId"`
}

// ListArtifacts pages through GET /search/artifacts (used by argus reconcile).
func (c *Client) ListArtifacts(ctx context.Context) ([]Artifact, error) {
	var all []Artifact
	for offset := 0; ; offset += 100 {
		var res struct {
			Count     int        `json:"count"`
			Artifacts []Artifact `json:"artifacts"`
		}
		if err := c.do(ctx, http.MethodGet, fmt.Sprintf("/search/artifacts?limit=100&offset=%d", offset), nil, &res); err != nil {
			return nil, err
		}
		all = append(all, res.Artifacts...)
		if len(res.Artifacts) < 100 {
			return all, nil
		}
	}
}

// VersionLabels returns the labels of one version (git.sha holds the full commit SHA).
func (c *Client) VersionLabels(ctx context.Context, group, artifact, version string) (map[string]string, error) {
	var meta struct {
		Labels map[string]string `json:"labels"`
	}
	err := c.do(ctx, http.MethodGet, artifactPath(group, artifact)+"/versions/"+url.PathEscape(version), nil, &meta)
	return meta.Labels, err
}
