// Command lazycue runs a single self-healing browser test described in plain English.
//
// Usage:
//
//	lazycue [options] "test description"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	lazycue "github.com/boldsoftware/shelley/lazycue"
)

func main() {
	runTests()
}

func runTests() {
	baseURL := flag.String("base-url", "", "Base URL of the app under test (required)")
	cacheDir := flag.String("cache-dir", "", "Directory for cache JSON files (default: .lazycue)")
	model := flag.String("model", "", "LLM model (default: claude-sonnet-4-6)")
	apiURL := flag.String("api-url", "", "Anthropic API base URL (env: ANTHROPIC_BASE_URL)")
	apiKey := flag.String("api-key", "", "Anthropic API key (env: ANTHROPIC_API_KEY)")
	verbose := flag.Bool("verbose", false, "Verbose output")
	artifactDir := flag.String("artifact-dir", "", "Directory to write per-step screenshots and an HTML report (index.html)")
	videoDir := flag.String("video-dir", "", "Directory to write an MP4 per test (prompt title card + captioned screenshots). Env: LAZYCUE_VIDEO_DIR")
	jsonOut := flag.String("json", "", "Write a machine-readable JSON summary of the run to this file (for CI cache stats)")
	repoRoot := flag.String("repo-root", "", "Repository root for the heal agent's git_command tool (default: git repo root of the cwd)")

	flag.Parse()

	if *baseURL == "" {
		fmt.Fprintln(os.Stderr, "error: --base-url is required")
		os.Exit(2)
	}

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, `usage: lazycue [options] "test description"`)
		os.Exit(2)
	}
	description := args[0]

	resolvedCacheDir := *cacheDir
	if resolvedCacheDir == "" {
		resolvedCacheDir = ".lazycue"
	}

	resolvedVideoDir := *videoDir
	if resolvedVideoDir == "" {
		resolvedVideoDir = os.Getenv("LAZYCUE_VIDEO_DIR")
	}

	opts := lazycue.Options{
		BaseURL:          *baseURL,
		CacheDir:         resolvedCacheDir,
		Model:            *model,
		AnthropicBaseURL: *apiURL,
		AnthropicAPIKey:  *apiKey,
		Verbose:          *verbose,
		ArtifactDir:      *artifactDir,
		VideoDir:         resolvedVideoDir,
		RepoRoot:         *repoRoot,
	}

	ctx := context.Background()

	result, err := lazycue.Run(ctx, opts, description)
	if err != nil {
		printError(description, err)
		os.Exit(1)
	}

	printResult(result)
	results := []*lazycue.TestResult{result}

	// Render the MP4 (after the run, not in the per-test path) so the report can
	// embed it and the summary can record its path. Best-effort.
	if resolvedVideoDir != "" {
		if err := lazycue.RenderVideos(resolvedVideoDir, results); err != nil {
			fmt.Fprintf(os.Stderr, "warning: render video: %v\n", err)
		}
	}

	// Write an HTML report when artifacts are enabled.
	if *artifactDir != "" {
		if err := lazycue.WriteReport(*artifactDir, results); err != nil {
			fmt.Fprintf(os.Stderr, "warning: write report: %v\n", err)
		} else {
			fmt.Printf("\033[2mreport: %s/index.html\033[0m\n", *artifactDir)
		}
	}

	// Write a machine-readable JSON summary for CI cache-stats reporting.
	if *jsonOut != "" {
		if err := lazycue.WriteSummary(*jsonOut, results); err != nil {
			fmt.Fprintf(os.Stderr, "warning: write json summary: %v\n", err)
		}
	}

	if result.VideoPath != "" {
		fmt.Printf("\033[2mvideo: %s\033[0m\n", result.VideoPath)
	}

	if !result.Pass {
		os.Exit(1)
	}
}

func printResult(r *lazycue.TestResult) {
	// Status emoji + colour.
	var status, colour, reset string
	if r.Pass {
		status = "PASS"
		colour = "\033[32m" // green
	} else {
		status = "FAIL"
		colour = "\033[31m" // red
	}
	reset = "\033[0m"

	// Mode badge.
	var badge string
	switch r.Mode {
	case lazycue.RunModeCached:
		badge = fmt.Sprintf("cached v%d", r.CacheVersion)
	case lazycue.RunModeGenerated:
		badge = fmt.Sprintf("generated → v%d", r.CacheVersion)
	case lazycue.RunModeHealed:
		badge = fmt.Sprintf("healed → v%d", r.CacheVersion)
	}

	// Timing.
	totalMs := r.TotalDuration.Round(time.Millisecond)
	var timing string
	if r.AgentDuration > 0 {
		timing = fmt.Sprintf("%s total, %s agent", totalMs, r.AgentDuration.Round(time.Millisecond))
	} else {
		timing = totalMs.String()
	}

	// Header line.
	fmt.Printf("%s%s%s  [%s]  %s\n", colour, status, reset, badge, timing)

	// Description (dimmed).
	fmt.Printf("\033[2m  %s\033[0m\n", truncateDesc(r.Description, 120))

	// Steps.
	if len(r.Steps) > 0 {
		for _, s := range r.Steps {
			mark := "\033[32m✓\033[0m"
			if !s.Pass {
				mark = "\033[31m✗\033[0m"
			}
			line := fmt.Sprintf("  %s %-50s %6s", mark, s.Summary, s.Duration.Round(time.Millisecond))
			if s.Error != "" {
				line += fmt.Sprintf("  \033[31m%s\033[0m", truncateDesc(s.Error, 80))
			}
			fmt.Println(line)
		}
	}

	// Token usage.
	if r.InputTokens > 0 {
		fmt.Printf("\033[2m  ⚡ %s in / %s out tokens  ~$%.3f\033[0m\n", formatTokens(r.InputTokens), formatTokens(r.OutputTokens), r.EstimatedCost)
	}

	// Error detail for failures.
	if !r.Pass && r.Error != "" {
		errLines := strings.Split(r.Error, "\n")
		if len(errLines) <= 3 {
			fmt.Printf("\033[31m  %s\033[0m\n", r.Error)
		} else {
			for _, l := range errLines[:3] {
				fmt.Printf("\033[31m  %s\033[0m\n", l)
			}
			fmt.Printf("\033[2m  ... (%d more lines)\033[0m\n", len(errLines)-3)
		}
	}
}

func printError(desc string, err error) {
	fmt.Printf("\033[31mERROR\033[0m\n")
	fmt.Printf("\033[2m  %s\033[0m\n", truncateDesc(desc, 120))
	fmt.Printf("\033[31m  %s\033[0m\n", err)
}

func truncateDesc(s string, max int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// formatTokens formats an integer with comma separators: 14832 → "14,832".
func formatTokens(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}
