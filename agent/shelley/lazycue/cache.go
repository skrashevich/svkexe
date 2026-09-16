package lazycue

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CacheBanner is stored in the "_README" field of every cache file to make it
// clear the file is machine-managed and should not be hand-edited.
const CacheBanner = "This file is managed by LazyCue (see shelley/lazycue). Do NOT edit by hand — it is the cached, machine-generated DSL for a self-healing browser test. To change behavior, edit the test description and re-run LazyCue."

// CachedTest is the JSON wrapper stored in each cache file under .lazycue/.
type CachedTest struct {
	README      string          `json:"_README"`
	Description string          `json:"description"`
	Version     int             `json:"version"`
	Steps       json.RawMessage `json:"steps"`
	Metadata    *CacheMetadata  `json:"metadata,omitempty"`
}

// CacheMetadata holds provenance information about a cached test.
type CacheMetadata struct {
	CreatedAt        time.Time `json:"created_at"`
	Hostname         string    `json:"hostname"`
	Model            string    `json:"model"`
	InputTokens      int       `json:"input_tokens"`
	OutputTokens     int       `json:"output_tokens"`
	EstimatedCostUSD float64   `json:"estimated_cost_usd"`
	CIRun            string    `json:"ci_run,omitempty"`
	GitSHA           string    `json:"git_sha,omitempty"`
	Mode             string    `json:"mode"`
}

// CacheHit describes a cache hit.
type CacheHit struct {
	Version int
}

// detectCIRun returns the CI build URL from common CI env vars, or empty string.
func detectCIRun() string {
	if u := os.Getenv("BUILDKITE_BUILD_URL"); u != "" {
		return u
	}
	server := os.Getenv("GITHUB_SERVER_URL")
	repo := os.Getenv("GITHUB_REPOSITORY")
	runID := os.Getenv("GITHUB_RUN_ID")
	if server != "" && repo != "" && runID != "" {
		return server + "/" + repo + "/actions/runs/" + runID
	}
	if u := os.Getenv("CI_JOB_URL"); u != "" {
		return u
	}
	return ""
}

// detectGitSHA returns the current HEAD commit SHA (in CWD), or empty string on error.
func detectGitSHA() string {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// DescriptionHash returns the first 16 hex chars of the SHA-256 hash of a description.
func DescriptionHash(description string) string {
	h := sha256.Sum256([]byte(description))
	return fmt.Sprintf("%x", h[:8])
}

// CacheFilePath returns the path to the cache file for a description.
func CacheFilePath(cacheDir, description string) string {
	return filepath.Join(cacheDir, DescriptionHash(description)+".json")
}

// GetCachedTest reads the cached DSL test for a description from cacheDir.
// Returns (nil, nil, nil) if no cache file exists.
func GetCachedTest(cacheDir, description string) (*CachedTest, *CacheHit, error) {
	path := CacheFilePath(cacheDir, description)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read cache file %s: %w", path, err)
	}

	var cached CachedTest
	if err := json.Unmarshal(data, &cached); err != nil {
		return nil, nil, fmt.Errorf("unmarshal cache file %s: %w", path, err)
	}
	return &cached, &CacheHit{Version: cached.Version}, nil
}

// SaveCachedTest writes the cached DSL test for a description to cacheDir as a
// pretty-printed JSON file, including the managed-by banner.
func SaveCachedTest(cacheDir, description string, steps []byte, version int, meta *CacheMetadata) error {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return fmt.Errorf("mkdir cache dir %s: %w", cacheDir, err)
	}

	wrapped := CachedTest{
		README:      CacheBanner,
		Description: description,
		Version:     version,
		Steps:       json.RawMessage(steps),
		Metadata:    meta,
	}
	blob, err := json.MarshalIndent(wrapped, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cached test: %w", err)
	}
	blob = append(blob, '\n')

	path := CacheFilePath(cacheDir, description)
	if err := os.WriteFile(path, blob, 0o644); err != nil {
		return fmt.Errorf("write cache file %s: %w", path, err)
	}
	return nil
}
