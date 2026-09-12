package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/common"
)

const defaultAPIBase = "https://api.github.com"
const defaultMaxDownload = 500 << 20 // 500 MiB
const maxReleaseListSize = 50
const defaultReleaseListSize = 20

// ErrNoReleases is returned when the repo has no published GitHub releases
// (GitHub API /releases/latest responds 404).
var ErrNoReleases = errors.New("no releases found for repository")

// GitHubClient is the interface for interacting with GitHub releases.
type GitHubClient interface {
	FetchLatestRelease(ctx context.Context, repo string) (*ReleaseInfo, error)
	ListReleases(ctx context.Context, repo string, limit int) ([]ReleaseSummary, error)
	FetchReleaseByTag(ctx context.Context, repo, tag string) (*ReleaseInfo, error)
	Download(ctx context.Context, url, dest string, maxSize int64) error
	FetchBytes(ctx context.Context, url string, maxSize int64) ([]byte, error)
}

// HTTPGitHubClient is the production implementation of GitHubClient.
type HTTPGitHubClient struct {
	// APIBase is the GitHub API root. It can be overridden in tests.
	APIBase    string
	token      string
	httpClient *http.Client
}

// NewHTTPGitHubClient creates a new HTTPGitHubClient.
// Pass nil for httpClient to use http.DefaultClient.
func NewHTTPGitHubClient(token string, httpClient *http.Client) *HTTPGitHubClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &HTTPGitHubClient{
		APIBase:    defaultAPIBase,
		token:      token,
		httpClient: httpClient,
	}
}

// FetchLatestRelease fetches the latest release metadata for the given repo.
func (c *HTTPGitHubClient) FetchLatestRelease(ctx context.Context, repo string) (*ReleaseInfo, error) {
	var rel ReleaseInfo
	if err := c.fetchAPIJSON(ctx, fmt.Sprintf("/repos/%s/releases/latest", repo), &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

// ListReleases returns published releases for the configured repository.
func (c *HTTPGitHubClient) ListReleases(ctx context.Context, repo string, limit int) ([]ReleaseSummary, error) {
	if limit <= 0 {
		limit = defaultReleaseListSize
	}
	if limit > maxReleaseListSize {
		limit = maxReleaseListSize
	}

	var releases []struct {
		ReleaseSummary
		Draft bool `json:"draft"`
	}
	path := fmt.Sprintf("/repos/%s/releases?per_page=%s", repo, strconv.Itoa(limit))
	if err := c.fetchAPIJSON(ctx, path, &releases); err != nil {
		return nil, err
	}

	summaries := make([]ReleaseSummary, 0, len(releases))
	for _, release := range releases {
		if release.Draft || !isSafeReleaseTag(release.TagName) {
			continue
		}
		summaries = append(summaries, release.ReleaseSummary)
	}
	return summaries, nil
}

// FetchReleaseByTag resolves a release server-side so clients cannot supply assets.
func (c *HTTPGitHubClient) FetchReleaseByTag(ctx context.Context, repo, tag string) (*ReleaseInfo, error) {
	if !isSafeReleaseTag(tag) {
		return nil, errors.New("invalid release tag")
	}

	var rel ReleaseInfo
	path := fmt.Sprintf("/repos/%s/releases/tags/%s", repo, url.PathEscape(tag))
	if err := c.fetchAPIJSON(ctx, path, &rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (c *HTTPGitHubClient) fetchAPIJSON(ctx context.Context, path string, target any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.APIBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "new-api-selfupdate")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNoReleases
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		return fmt.Errorf("github API %d: %s", resp.StatusCode, snippet)
	}
	return common.Unmarshal(body, target)
}

// FetchBytes downloads url into memory (up to maxSize bytes).
// The URL is validated with ValidateDownloadURL before any request is made.
func (c *HTTPGitHubClient) FetchBytes(ctx context.Context, url string, maxSize int64) ([]byte, error) {
	if err := ValidateDownloadURL(url); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "new-api-selfupdate")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	return io.ReadAll(io.LimitReader(resp.Body, maxSize))
}

// Download downloads url to the local file at dest (up to maxSize bytes).
// The URL is validated with ValidateDownloadURL before any request is made.
func (c *HTTPGitHubClient) Download(ctx context.Context, url, dest string, maxSize int64) error {
	if err := ValidateDownloadURL(url); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "new-api-selfupdate")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download %s: HTTP %d", url, resp.StatusCode)
	}

	f, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := io.Copy(f, io.LimitReader(resp.Body, maxSize)); err != nil {
		return err
	}
	return nil
}
