package updater

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skrashevich/svkexe/internal/version"
)

const (
	remoteSHA = "1111111111111111111111111111111111111111"
	localSHA  = "2222222222222222222222222222222222222222"
)

const branchBody = `{
  "sha": "` + remoteSHA + `",
  "html_url": "https://github.com/skrashevich/svkexe/commit/` + remoteSHA + `",
  "commit": {"committer": {"date": "2026-09-01T10:00:00Z"}}
}`

const releaseBody = `{
  "tag_name": "v1.4.0",
  "target_commitish": "` + remoteSHA + `",
  "published_at": "2026-09-02T11:00:00Z",
  "html_url": "https://github.com/skrashevich/svkexe/releases/tag/v1.4.0"
}`

// githubStub serves the two GitHub endpoints the checker uses and counts hits.
type githubStub struct {
	server       *httptest.Server
	branchHits   atomic.Int64
	releaseHits  atomic.Int64
	releaseCode  int    // status for the releases endpoint, 0 means 200
	branchCode   int    // status for the commits endpoint, 0 means 200
	branchBody   string // overrides the default commit payload when set
	lastUA       atomic.Value
	lastAccept   atomic.Value
	lastAuthHdr  atomic.Value
	handlerDelay time.Duration
}

func newGitHubStub(t *testing.T) *githubStub {
	t.Helper()
	s := &githubStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/skrashevich/svkexe/commits/", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		s.branchHits.Add(1)
		if s.handlerDelay > 0 {
			select {
			case <-time.After(s.handlerDelay):
			case <-r.Context().Done():
				return
			}
		}
		if s.branchCode != 0 && s.branchCode != http.StatusOK {
			w.WriteHeader(s.branchCode)
			_, _ = io.WriteString(w, `{"message":"branch boom"}`)
			return
		}
		body := s.branchBody
		if body == "" {
			body = branchBody
		}
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/repos/skrashevich/svkexe/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		s.releaseHits.Add(1)
		if s.releaseCode != 0 && s.releaseCode != http.StatusOK {
			w.WriteHeader(s.releaseCode)
			_, _ = io.WriteString(w, `{"message":"Not Found"}`)
			return
		}
		_, _ = io.WriteString(w, releaseBody)
	})
	s.server = httptest.NewServer(mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *githubStub) record(r *http.Request) {
	s.lastUA.Store(r.Header.Get("User-Agent"))
	s.lastAccept.Store(r.Header.Get("Accept"))
	s.lastAuthHdr.Store(r.Header.Get("Authorization"))
}

func stampedInfo() version.Info {
	return version.Info{Version: "v1.3.0", Commit: localSHA, BuildDate: "2026-08-01T00:00:00Z"}
}

func devInfo() version.Info {
	return version.Info{Version: "dev", Commit: "unknown", BuildDate: "unknown"}
}

func newTestChecker(t *testing.T, stub *githubStub, ch Channel, local version.Info) *Checker {
	t.Helper()
	return NewChecker(Config{
		Channel: ch,
		APIBase: stub.server.URL,
		Local:   &local,
	})
}

func TestCheckerChannels(t *testing.T) {
	tests := []struct {
		name        string
		channel     Channel
		releaseCode int
		wantVersion string
		wantCommit  string
		wantChannel Channel
		wantURL     string
	}{
		{
			name:        "branch channel",
			channel:     ChannelBranch,
			wantVersion: "1111111",
			wantCommit:  remoteSHA,
			wantChannel: ChannelBranch,
			wantURL:     "https://github.com/skrashevich/svkexe/commit/" + remoteSHA,
		},
		{
			name:        "release channel",
			channel:     ChannelRelease,
			wantVersion: "v1.4.0",
			wantCommit:  remoteSHA,
			wantChannel: ChannelRelease,
			wantURL:     "https://github.com/skrashevich/svkexe/releases/tag/v1.4.0",
		},
		{
			name:        "release 404 falls back to branch",
			channel:     ChannelRelease,
			releaseCode: http.StatusNotFound,
			wantVersion: "1111111",
			wantCommit:  remoteSHA,
			wantChannel: ChannelBranch,
			wantURL:     "https://github.com/skrashevich/svkexe/commit/" + remoteSHA,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub(t)
			stub.releaseCode = tc.releaseCode
			c := newTestChecker(t, stub, tc.channel, stampedInfo())

			rel, err := c.Check(t.Context(), false)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if rel.Version != tc.wantVersion {
				t.Errorf("Version = %q, want %q", rel.Version, tc.wantVersion)
			}
			if rel.Commit != tc.wantCommit {
				t.Errorf("Commit = %q, want %q", rel.Commit, tc.wantCommit)
			}
			if rel.Channel != tc.wantChannel {
				t.Errorf("Channel = %q, want %q", rel.Channel, tc.wantChannel)
			}
			if rel.URL != tc.wantURL {
				t.Errorf("URL = %q, want %q", rel.URL, tc.wantURL)
			}
			if rel.PublishedAt.IsZero() {
				t.Error("PublishedAt is zero")
			}
			if got := stub.lastUA.Load(); got != userAgent {
				t.Errorf("User-Agent = %v, want %q", got, userAgent)
			}
			if got := stub.lastAccept.Load(); got != "application/vnd.github+json" {
				t.Errorf("Accept = %v", got)
			}
		})
	}
}

func TestCheckerReleaseTagOnly(t *testing.T) {
	stub := newGitHubStub(t)
	c := newTestChecker(t, stub, ChannelRelease, stampedInfo())

	rel, err := c.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Tag != "v1.4.0" {
		t.Errorf("Tag = %q, want v1.4.0", rel.Tag)
	}
	if stub.branchHits.Load() != 0 {
		t.Errorf("branch endpoint hit %d times, want 0", stub.branchHits.Load())
	}
}

func TestCheckerSendsToken(t *testing.T) {
	stub := newGitHubStub(t)
	local := stampedInfo()
	c := NewChecker(Config{APIBase: stub.server.URL, Local: &local, Token: "ghp_secret"})

	if _, err := c.Check(t.Context(), false); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := stub.lastAuthHdr.Load(); got != "Bearer ghp_secret" {
		t.Errorf("Authorization = %v, want Bearer ghp_secret", got)
	}
}

func TestCheckerErrors(t *testing.T) {
	tests := []struct {
		name        string
		branchCode  int
		branchBody  string
		wantErrPart string
	}{
		{name: "server error", branchCode: http.StatusInternalServerError, wantErrPart: "500"},
		{name: "rate limited", branchCode: http.StatusForbidden, wantErrPart: "403"},
		{name: "invalid json", branchBody: "not json at all", wantErrPart: "decode branch"},
		{name: "missing sha", branchBody: `{"html_url":"x"}`, wantErrPart: "no commit sha"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub(t)
			stub.branchCode = tc.branchCode
			stub.branchBody = tc.branchBody
			c := newTestChecker(t, stub, ChannelBranch, stampedInfo())

			_, err := c.Check(t.Context(), false)
			if err == nil {
				t.Fatal("Check succeeded, want error")
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Errorf("error = %q, want it to contain %q", err, tc.wantErrPart)
			}
		})
	}
}

func TestCheckerErrorSnippetIsBounded(t *testing.T) {
	stub := newGitHubStub(t)
	stub.branchCode = http.StatusBadGateway
	c := newTestChecker(t, stub, ChannelBranch, stampedInfo())

	_, err := c.Check(t.Context(), false)
	if err == nil {
		t.Fatal("Check succeeded, want error")
	}
	if len(err.Error()) > 1024 {
		t.Errorf("error message is %d bytes, want a bounded snippet", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "branch boom") {
		t.Errorf("error = %q, want it to quote the response body", err)
	}
}

func TestCheckerCaching(t *testing.T) {
	stub := newGitHubStub(t)
	c := newTestChecker(t, stub, ChannelBranch, stampedInfo())

	for range 3 {
		if _, err := c.Check(t.Context(), false); err != nil {
			t.Fatalf("Check: %v", err)
		}
	}
	if got := stub.branchHits.Load(); got != 1 {
		t.Fatalf("branch endpoint hit %d times, want 1 (cached)", got)
	}

	if _, err := c.Check(t.Context(), true); err != nil {
		t.Fatalf("forced Check: %v", err)
	}
	if got := stub.branchHits.Load(); got != 2 {
		t.Fatalf("branch endpoint hit %d times after force, want 2", got)
	}
}

func TestCheckerCacheExpires(t *testing.T) {
	stub := newGitHubStub(t)
	local := stampedInfo()
	c := NewChecker(Config{APIBase: stub.server.URL, Local: &local, CacheTTL: time.Millisecond})

	if _, err := c.Check(t.Context(), false); err != nil {
		t.Fatalf("Check: %v", err)
	}
	// Sleep past the TTL rather than relying on two clock reads differing:
	// a sub-tick TTL makes the assertion depend on the platform's clock
	// resolution instead of on the cache logic.
	time.Sleep(5 * time.Millisecond)
	if _, err := c.Check(t.Context(), false); err != nil {
		t.Fatalf("Check: %v", err)
	}
	if got := stub.branchHits.Load(); got != 2 {
		t.Fatalf("branch endpoint hit %d times, want 2 (TTL expired)", got)
	}
}

func TestCheckerStatus(t *testing.T) {
	tests := []struct {
		name       string
		channel    Channel
		local      version.Info
		wantUpdate bool
		wantReason bool
	}{
		{
			name:       "branch update available",
			channel:    ChannelBranch,
			local:      stampedInfo(),
			wantUpdate: true,
		},
		{
			name:    "branch up to date",
			channel: ChannelBranch,
			local:   version.Info{Version: "v1.3.0", Commit: remoteSHA},
		},
		{
			name:    "branch up to date, different sha case",
			channel: ChannelBranch,
			local:   version.Info{Version: "v1.3.0", Commit: strings.ToUpper(remoteSHA)},
		},
		{
			name:       "branch dev build cannot compare",
			channel:    ChannelBranch,
			local:      devInfo(),
			wantReason: true,
		},
		{
			name:       "release update available",
			channel:    ChannelRelease,
			local:      stampedInfo(),
			wantUpdate: true,
		},
		{
			name:    "release up to date",
			channel: ChannelRelease,
			local:   version.Info{Version: "v1.4.0", Commit: localSHA},
		},
		{
			name:       "release dev build cannot compare",
			channel:    ChannelRelease,
			local:      devInfo(),
			wantReason: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			stub := newGitHubStub(t)
			c := newTestChecker(t, stub, tc.channel, tc.local)

			st, err := c.Status(t.Context(), false)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if st.UpdateAvailable != tc.wantUpdate {
				t.Errorf("UpdateAvailable = %v, want %v (reason %q)", st.UpdateAvailable, tc.wantUpdate, st.Reason)
			}
			if (st.Reason != "") != tc.wantReason {
				t.Errorf("Reason = %q, want present=%v", st.Reason, tc.wantReason)
			}
			if st.Latest == nil {
				t.Fatal("Latest is nil")
			}
			if st.Current != tc.local {
				t.Errorf("Current = %+v, want %+v", st.Current, tc.local)
			}
			if st.CheckedAt.IsZero() {
				t.Error("CheckedAt is zero")
			}
		})
	}
}

func TestCheckerStatusPropagatesError(t *testing.T) {
	stub := newGitHubStub(t)
	stub.branchCode = http.StatusInternalServerError
	c := newTestChecker(t, stub, ChannelBranch, stampedInfo())

	st, err := c.Status(t.Context(), false)
	if err == nil {
		t.Fatal("Status succeeded, want error")
	}
	if st.Latest != nil {
		t.Errorf("Latest = %+v, want nil on error", st.Latest)
	}
	if st.UpdateAvailable {
		t.Error("UpdateAvailable is true on error")
	}
}

func TestCheckerContextCancellation(t *testing.T) {
	stub := newGitHubStub(t)
	stub.handlerDelay = 2 * time.Second
	c := newTestChecker(t, stub, ChannelBranch, stampedInfo())

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := c.Check(ctx, false)
	if err == nil {
		t.Fatal("Check succeeded, want context error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want it to wrap context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Check took %v, want it to honour the context deadline", elapsed)
	}
}

func TestCheckerDefaults(t *testing.T) {
	c := NewChecker(Config{})
	cfg := c.Config()
	if cfg.Owner != DefaultOwner || cfg.Repo != DefaultRepo || cfg.Branch != DefaultBranch {
		t.Errorf("repo defaults = %s/%s@%s", cfg.Owner, cfg.Repo, cfg.Branch)
	}
	if cfg.Channel != ChannelBranch {
		t.Errorf("Channel = %q, want %q", cfg.Channel, ChannelBranch)
	}
	if cfg.APIBase != DefaultAPIBase {
		t.Errorf("APIBase = %q, want %q", cfg.APIBase, DefaultAPIBase)
	}
	if cfg.CacheTTL != DefaultCacheTTL {
		t.Errorf("CacheTTL = %v, want %v", cfg.CacheTTL, DefaultCacheTTL)
	}
	if cfg.HTTPClient == nil || cfg.HTTPClient.Timeout <= 0 {
		t.Error("HTTPClient has no timeout")
	}
}

func TestNewConfigFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		wantTTL  time.Duration
		wantChan Channel
		wantBase string
		wantTok  string
	}{
		{
			name:     "defaults",
			wantTTL:  DefaultCacheTTL,
			wantChan: ChannelBranch,
			wantBase: DefaultAPIBase,
		},
		{
			name: "full override",
			env: map[string]string{
				"SVKEXE_UPDATE_OWNER":     "acme",
				"SVKEXE_UPDATE_REPO":      "widget",
				"SVKEXE_UPDATE_BRANCH":    "stable",
				"SVKEXE_UPDATE_CHANNEL":   "release",
				"SVKEXE_UPDATE_API_BASE":  "http://localhost:9999/",
				"SVKEXE_UPDATE_CACHE_TTL": "90s",
				"SVKEXE_GITHUB_TOKEN":     "tok1",
			},
			wantTTL:  90 * time.Second,
			wantChan: ChannelRelease,
			wantBase: "http://localhost:9999",
			wantTok:  "tok1",
		},
		{
			name:     "bare seconds ttl",
			env:      map[string]string{"SVKEXE_UPDATE_CACHE_TTL": "120"},
			wantTTL:  2 * time.Minute,
			wantChan: ChannelBranch,
			wantBase: DefaultAPIBase,
		},
		{
			name:     "unparseable ttl keeps default",
			env:      map[string]string{"SVKEXE_UPDATE_CACHE_TTL": "soon"},
			wantTTL:  DefaultCacheTTL,
			wantChan: ChannelBranch,
			wantBase: DefaultAPIBase,
		},
		{
			name:     "unknown channel falls back to branch",
			env:      map[string]string{"SVKEXE_UPDATE_CHANNEL": "nightly"},
			wantTTL:  DefaultCacheTTL,
			wantChan: ChannelBranch,
			wantBase: DefaultAPIBase,
		},
		{
			name:     "github token fallback",
			env:      map[string]string{"GITHUB_TOKEN": "tok2"},
			wantTTL:  DefaultCacheTTL,
			wantChan: ChannelBranch,
			wantBase: DefaultAPIBase,
			wantTok:  "tok2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range []string{
				"SVKEXE_UPDATE_OWNER", "SVKEXE_UPDATE_REPO", "SVKEXE_UPDATE_BRANCH",
				"SVKEXE_UPDATE_CHANNEL", "SVKEXE_UPDATE_API_BASE", "SVKEXE_UPDATE_CACHE_TTL",
				"SVKEXE_GITHUB_TOKEN", "GITHUB_TOKEN",
			} {
				t.Setenv(k, "")
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			cfg := NewConfigFromEnv()
			if cfg.CacheTTL != tc.wantTTL {
				t.Errorf("CacheTTL = %v, want %v", cfg.CacheTTL, tc.wantTTL)
			}
			if cfg.Channel != tc.wantChan {
				t.Errorf("Channel = %q, want %q", cfg.Channel, tc.wantChan)
			}
			if cfg.APIBase != tc.wantBase {
				t.Errorf("APIBase = %q, want %q", cfg.APIBase, tc.wantBase)
			}
			if cfg.Token != tc.wantTok {
				t.Errorf("Token = %q, want %q", cfg.Token, tc.wantTok)
			}
			if cfg.Owner == "" || cfg.Repo == "" || cfg.Branch == "" {
				t.Errorf("repo coordinates incomplete: %s/%s@%s", cfg.Owner, cfg.Repo, cfg.Branch)
			}
		})
	}
}

func TestNewConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("SVKEXE_UPDATE_OWNER", "acme")
	t.Setenv("SVKEXE_UPDATE_REPO", "widget")
	t.Setenv("SVKEXE_UPDATE_BRANCH", "stable")

	cfg := NewConfigFromEnv()
	if cfg.Owner != "acme" || cfg.Repo != "widget" || cfg.Branch != "stable" {
		t.Fatalf("got %s/%s@%s, want acme/widget@stable", cfg.Owner, cfg.Repo, cfg.Branch)
	}
}

func TestSnippetBounded(t *testing.T) {
	long := strings.Repeat("a", maxErrorSnippet*3)
	got := snippet([]byte(long))
	if len(got) > maxErrorSnippet+3 {
		t.Errorf("snippet is %d bytes, want <= %d", len(got), maxErrorSnippet+3)
	}
	if got := snippet([]byte("   \n ")); got != "(empty body)" {
		t.Errorf("snippet of blank body = %q", got)
	}
	if got := snippet([]byte("line one\nline two")); got != "line one line two" {
		t.Errorf("snippet did not flatten newlines: %q", got)
	}
}

func TestIsSHA(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{remoteSHA, true},
		{strings.ToUpper(remoteSHA), true},
		{"main", false},
		{"", false},
		{strings.Repeat("z", 40), false},
		{remoteSHA[:39], false},
	}
	for _, tc := range tests {
		if got := isSHA(tc.in); got != tc.want {
			t.Errorf("isSHA(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestReleaseWithBranchTargetHasNoCommit(t *testing.T) {
	// A release cut from a branch reports the branch name in target_commitish;
	// that is not a comparable commit, so Commit must stay empty.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"tag_name":"v2.0.0","target_commitish":"main","published_at":"2026-09-02T11:00:00Z","html_url":"u"}`)
	}))
	defer srv.Close()

	local := stampedInfo()
	c := NewChecker(Config{Channel: ChannelRelease, APIBase: srv.URL, Local: &local})
	rel, err := c.Check(t.Context(), false)
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel.Commit != "" {
		t.Errorf("Commit = %q, want empty for a branch target_commitish", rel.Commit)
	}
}
