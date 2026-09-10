// Package updater tells the gateway whether a newer svkexe build exists and
// triggers the privileged update that installs it.
//
// The two halves are deliberately independent: Checker only talks to the
// GitHub API, Runner only talks to the filesystem. A deployment that cannot
// self-update (a container image, a read-only host) still gets a working
// update check.
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/skrashevich/svkexe/internal/version"
)

// Channel selects what "latest" means for a deployment.
type Channel string

const (
	// ChannelBranch tracks the tip of a git branch. This is the default because
	// svkexe is deployed straight from main.
	ChannelBranch Channel = "branch"
	// ChannelRelease tracks the newest published GitHub release.
	ChannelRelease Channel = "release"
)

// Default configuration values, also used by NewConfigFromEnv.
const (
	DefaultOwner    = "skrashevich"
	DefaultRepo     = "svkexe"
	DefaultBranch   = "main"
	DefaultAPIBase  = "https://api.github.com"
	DefaultCacheTTL = 15 * time.Minute

	// userAgent is sent on every request; the GitHub API rejects requests
	// without one.
	userAgent = "svkexe-updater/1"
	// maxBodyBytes caps how much of a GitHub response we are willing to buffer.
	maxBodyBytes = 1 << 20
	// maxErrorSnippet caps how much of a failing response we quote in errors,
	// so a rate-limit HTML page cannot flood the logs.
	maxErrorSnippet = 256
)

// Config describes where the checker looks for new builds.
type Config struct {
	// Owner is the GitHub account holding the repository. Defaults to
	// DefaultOwner.
	Owner string
	// Repo is the GitHub repository name. Defaults to DefaultRepo.
	Repo string
	// Branch is the branch tracked by ChannelBranch. Defaults to DefaultBranch.
	Branch string
	// Channel selects branch or release tracking. Defaults to ChannelBranch.
	Channel Channel
	// APIBase is the GitHub API root. Overridable so tests can point at an
	// httptest server.
	APIBase string
	// HTTPClient performs the requests. Defaults to a client with a timeout.
	HTTPClient *http.Client
	// CacheTTL is how long a successful result is reused. Defaults to
	// DefaultCacheTTL.
	CacheTTL time.Duration
	// Token is an optional GitHub token. Unauthenticated API calls are limited
	// to 60 requests per hour per IP, which a busy dashboard can exhaust.
	Token string
	// Local overrides the build metadata the remote result is compared against.
	// nil means version.Get(); tests inject a fake.
	Local *version.Info
}

func (c Config) withDefaults() Config {
	if c.Owner == "" {
		c.Owner = DefaultOwner
	}
	if c.Repo == "" {
		c.Repo = DefaultRepo
	}
	if c.Branch == "" {
		c.Branch = DefaultBranch
	}
	if c.Channel == "" {
		c.Channel = ChannelBranch
	}
	if c.APIBase == "" {
		c.APIBase = DefaultAPIBase
	}
	c.APIBase = strings.TrimRight(c.APIBase, "/")
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 15 * time.Second}
	}
	if c.CacheTTL <= 0 {
		c.CacheTTL = DefaultCacheTTL
	}
	return c
}

// NewConfigFromEnv builds a Config from the SVKEXE_UPDATE_* environment
// variables, falling back to the documented defaults:
//
//	SVKEXE_UPDATE_OWNER      (default "skrashevich")
//	SVKEXE_UPDATE_REPO       (default "svkexe")
//	SVKEXE_UPDATE_BRANCH     (default "main")
//	SVKEXE_UPDATE_CHANNEL    "branch" (default) or "release"
//	SVKEXE_UPDATE_API_BASE   (default "https://api.github.com")
//	SVKEXE_UPDATE_CACHE_TTL  Go duration or bare seconds (default 15m)
//	SVKEXE_GITHUB_TOKEN      falls back to GITHUB_TOKEN
//
// Unparseable values are ignored in favour of the default: a typo in an
// optional tuning knob must not take the update check offline.
func NewConfigFromEnv() Config {
	cfg := Config{
		Owner:   os.Getenv("SVKEXE_UPDATE_OWNER"),
		Repo:    os.Getenv("SVKEXE_UPDATE_REPO"),
		Branch:  os.Getenv("SVKEXE_UPDATE_BRANCH"),
		Channel: parseChannel(os.Getenv("SVKEXE_UPDATE_CHANNEL")),
		APIBase: os.Getenv("SVKEXE_UPDATE_API_BASE"),
		Token:   os.Getenv("SVKEXE_GITHUB_TOKEN"),
	}
	if cfg.Token == "" {
		cfg.Token = os.Getenv("GITHUB_TOKEN")
	}
	if raw := os.Getenv("SVKEXE_UPDATE_CACHE_TTL"); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			cfg.CacheTTL = d
		} else if secs, err := strconv.Atoi(raw); err == nil && secs > 0 {
			cfg.CacheTTL = time.Duration(secs) * time.Second
		}
	}
	return cfg.withDefaults()
}

// parseChannel maps a configuration string to a Channel, returning
// ChannelBranch for anything unrecognised.
func parseChannel(s string) Channel {
	if strings.EqualFold(strings.TrimSpace(s), string(ChannelRelease)) {
		return ChannelRelease
	}
	return ChannelBranch
}

// Release is the newest build the checker found upstream.
type Release struct {
	// Version is the display name: the tag for releases, the short SHA for
	// branches.
	Version string `json:"version"`
	// Tag is the release tag, empty on the branch channel.
	Tag string `json:"tag,omitempty"`
	// Commit is the full SHA, empty when a release does not expose one.
	Commit string `json:"commit,omitempty"`
	// PublishedAt is the commit or release timestamp.
	PublishedAt time.Time `json:"publishedAt"`
	// URL is the GitHub web page for the commit or release.
	URL string `json:"url"`
	// Channel is the channel the result actually came from, which differs from
	// the configured channel when the release lookup fell back to the branch.
	Channel Channel `json:"channel"`
}

// Status is a comparison between the running binary and the newest upstream
// build, shaped for direct JSON delivery to the dashboard.
type Status struct {
	Current         version.Info `json:"current"`
	Latest          *Release     `json:"latest,omitempty"`
	UpdateAvailable bool         `json:"updateAvailable"`
	CheckedAt       time.Time    `json:"checkedAt"`
	// Reason explains why no comparison was possible, e.g. an unstamped dev
	// build. Empty when the comparison was meaningful.
	Reason string `json:"reason,omitempty"`
}

// Checker queries GitHub for the newest svkexe build and compares it against
// the running binary. It is safe for concurrent use.
type Checker struct {
	cfg   Config
	local version.Info

	mu     sync.Mutex
	cached *Release
	// cachedAt keeps its monotonic reading so TTL arithmetic is immune to wall
	// clock jumps (NTP steps, VM suspend). Callers get the UTC wall time.
	cachedAt time.Time
	// A failure is cached too, briefly. The System page checks on load, so
	// without this an admin refreshing during a GitHub outage — or after being
	// rate-limited — spends the whole 60-requests-per-hour budget on retries.
	failure   error
	failureAt time.Time
}

// negativeCacheTTL bounds how long a failed lookup suppresses retries. Short
// enough that a recovered API is picked up almost immediately, long enough that
// a reload loop cannot hammer it.
const negativeCacheTTL = 30 * time.Second

// NewChecker returns a Checker with cfg's zero fields filled in from the
// documented defaults.
func NewChecker(cfg Config) *Checker {
	cfg = cfg.withDefaults()
	local := version.Get()
	if cfg.Local != nil {
		local = *cfg.Local
	}
	return &Checker{cfg: cfg, local: local}
}

// Config returns the effective configuration, with defaults applied.
func (c *Checker) Config() Config { return c.cfg }

// Check returns the newest upstream build. Results are cached for the
// configured TTL; force skips the cache and always hits the API.
func (c *Checker) Check(ctx context.Context, force bool) (*Release, error) {
	rel, _, err := c.check(ctx, force)
	return rel, err
}

func (c *Checker) check(ctx context.Context, force bool) (*Release, time.Time, error) {
	// The lock is held across the fetch so that a burst of dashboard requests
	// results in one API call rather than one per request.
	c.mu.Lock()
	defer c.mu.Unlock()

	if !force && c.cached != nil && time.Since(c.cachedAt) < c.cfg.CacheTTL {
		return c.cached, c.cachedAt.UTC(), nil
	}
	if !force && c.failure != nil && time.Since(c.failureAt) < negativeCacheTTL {
		return nil, time.Time{}, c.failure
	}

	var (
		rel *Release
		err error
	)
	if c.cfg.Channel == ChannelRelease {
		rel, err = c.fetchRelease(ctx)
	} else {
		rel, err = c.fetchBranch(ctx)
	}
	if err != nil {
		c.failure, c.failureAt = err, time.Now()
		return nil, time.Time{}, err
	}

	c.cached, c.cachedAt = rel, time.Now()
	c.failure, c.failureAt = nil, time.Time{}
	return rel, c.cachedAt.UTC(), nil
}

// Status compares the newest upstream build against the running binary.
func (c *Checker) Status(ctx context.Context, force bool) (Status, error) {
	rel, at, err := c.check(ctx, force)
	if err != nil {
		return Status{Current: c.local, CheckedAt: time.Now().UTC()}, err
	}

	st := Status{Current: c.local, Latest: rel, CheckedAt: at}
	switch rel.Channel {
	case ChannelRelease:
		if c.local.Version == "" || c.local.Version == "dev" {
			st.Reason = "local build has no release version to compare; rebuild with the release ldflags to enable update checks"
			return st, nil
		}
		st.UpdateAvailable = rel.Tag != "" && !describesAtOrAfter(c.local.Version, rel.Tag)
	default:
		if c.local.IsDev() {
			st.Reason = "local build has no commit stamp to compare against " + c.cfg.Owner + "/" + c.cfg.Repo + "@" + c.cfg.Branch
			return st, nil
		}
		st.UpdateAvailable = rel.Commit != "" && !strings.EqualFold(rel.Commit, c.local.Commit)
	}
	return st, nil
}

// githubCommit is the subset of the commits API response we consume.
type githubCommit struct {
	SHA    string `json:"sha"`
	URL    string `json:"html_url"`
	Commit struct {
		Committer struct {
			Date time.Time `json:"date"`
		} `json:"committer"`
	} `json:"commit"`
}

// githubRelease is the subset of the releases API response we consume.
type githubRelease struct {
	TagName         string    `json:"tag_name"`
	TargetCommitish string    `json:"target_commitish"`
	PublishedAt     time.Time `json:"published_at"`
	URL             string    `json:"html_url"`
}

func (c *Checker) fetchBranch(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/commits/%s", c.cfg.APIBase, c.cfg.Owner, c.cfg.Repo, c.cfg.Branch)
	body, _, err := c.get(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("fetch branch %s: %w", c.cfg.Branch, err)
	}
	var gc githubCommit
	if err := json.Unmarshal(body, &gc); err != nil {
		return nil, fmt.Errorf("decode branch %s: %w", c.cfg.Branch, err)
	}
	if gc.SHA == "" {
		return nil, fmt.Errorf("decode branch %s: response has no commit sha", c.cfg.Branch)
	}
	return &Release{
		Version:     shortSHA(gc.SHA),
		Commit:      gc.SHA,
		PublishedAt: gc.Commit.Committer.Date,
		URL:         gc.URL,
		Channel:     ChannelBranch,
	}, nil
}

func (c *Checker) fetchRelease(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/%s/releases/latest", c.cfg.APIBase, c.cfg.Owner, c.cfg.Repo)
	body, status, err := c.get(ctx, url)
	if err != nil {
		// A repository that has never published a release answers 404. That is
		// the normal state of svkexe today, so fall back to the branch instead
		// of reporting the deployment as broken.
		if status == http.StatusNotFound {
			return c.fetchBranch(ctx)
		}
		return nil, fmt.Errorf("fetch latest release: %w", err)
	}
	var gr githubRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("decode latest release: %w", err)
	}
	if gr.TagName == "" {
		return nil, fmt.Errorf("decode latest release: response has no tag_name")
	}
	rel := &Release{
		Version:     gr.TagName,
		Tag:         gr.TagName,
		PublishedAt: gr.PublishedAt,
		URL:         gr.URL,
		Channel:     ChannelRelease,
	}
	// target_commitish is a SHA only when the release was cut from a detached
	// commit; for branch-based releases it holds the branch name, which is not
	// a commit we can compare against.
	if isSHA(gr.TargetCommitish) {
		rel.Commit = gr.TargetCommitish
	}
	return rel, nil
}

// get performs an authenticated GitHub API request. It returns the response
// body, the HTTP status code (0 when the request never completed) and an error
// for any non-2xx response.
func (c *Checker) get(ctx context.Context, url string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.Token)
	}

	resp, err := c.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("request %s: %w", url, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response from %s: %w", url, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, resp.StatusCode, fmt.Errorf("github returned %d for %s: %s", resp.StatusCode, url, snippet(body))
	}
	return body, resp.StatusCode, nil
}

// snippet renders a bounded, single-line excerpt of a response body for error
// messages.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > maxErrorSnippet {
		s = s[:maxErrorSnippet] + "..."
	}
	if s == "" {
		return "(empty body)"
	}
	return s
}

// describesAtOrAfter reports whether a `git describe --tags --always --dirty`
// string denotes the given tag or a commit built after it.
//
// describe renders an exact tag as "v1.2.3" and a later commit as
// "v1.2.3-4-gabc1234", optionally with a "-dirty" suffix. A plain string
// comparison would therefore keep offering "v1.2.3" to a build that is already
// four commits past it — reporting an update that would move the operator
// backwards. Anything that is not a describe of this tag (a bare SHA from a
// checkout without tags, or an older tag) is treated as behind, which errs
// toward offering the update rather than hiding it.
func describesAtOrAfter(local, tag string) bool {
	local = strings.TrimSuffix(local, "-dirty")
	if local == tag {
		return true
	}
	// "-N-g<sha>" is the only suffix describe appends to the tag itself.
	rest, ok := strings.CutPrefix(local, tag+"-")
	if !ok {
		return false
	}
	count, sha, ok := strings.Cut(rest, "-g")
	if !ok || count == "" || sha == "" {
		return false
	}
	_, err := strconv.Atoi(count)
	return err == nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// isSHA reports whether s looks like a full git object ID.
func isSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
