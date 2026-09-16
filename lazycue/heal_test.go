package lazycue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsSetsRepoRoot(t *testing.T) {
	var o Options
	o.defaults()
	if o.RepoRoot == "" {
		t.Fatal("defaults() left RepoRoot empty")
	}
	// In this repo, the root should contain a .git entry; at minimum it must be
	// an existing directory (the fallback "." is also acceptable).
	if _, err := os.Stat(o.RepoRoot); err != nil {
		t.Fatalf("RepoRoot %q does not exist: %v", o.RepoRoot, err)
	}
}

func TestDefaultsKeepsExplicitRepoRoot(t *testing.T) {
	o := Options{RepoRoot: "/custom/root"}
	o.defaults()
	if o.RepoRoot != "/custom/root" {
		t.Fatalf("RepoRoot = %q, want /custom/root", o.RepoRoot)
	}
}

func TestStepsAllPassed(t *testing.T) {
	ok := []StepResult{{Pass: true}, {Pass: true}}
	if !stepsAllPassed(ok, nil) {
		t.Error("all-passing results should report passed")
	}
	if stepsAllPassed(ok, os.ErrClosed) {
		t.Error("exec error should report not passed")
	}
	bad := []StepResult{{Pass: true}, {Pass: false}}
	if stepsAllPassed(bad, nil) {
		t.Error("a failing step should report not passed")
	}
}

func TestIsTimeoutFailure(t *testing.T) {
	timeoutRes := []StepResult{
		{Pass: true},
		{Pass: false, Error: "step 1 (wait_visible): timeout after 60s waiting for: ..."},
	}
	if !isTimeoutFailure(timeoutRes) {
		t.Error("a 'timeout after' error should be detected as a timeout failure")
	}
	otherRes := []StepResult{
		{Pass: false, Error: "eval: expected \"x\", got \"y\""},
	}
	if isTimeoutFailure(otherRes) {
		t.Error("a non-timeout error should not be detected as a timeout failure")
	}
	if isTimeoutFailure([]StepResult{{Pass: true}}) {
		t.Error("all-passing results should not be a timeout failure")
	}
}

func TestFailedStep(t *testing.T) {
	res := []StepResult{
		{Pass: true, Summary: "navigate /"},
		{Pass: false, Summary: "wait_visible #x", Error: "timeout after 10s"},
	}
	sum, errMsg := failedStep(res)
	if sum != "wait_visible #x" || !strings.Contains(errMsg, "timeout") {
		t.Fatalf("failedStep = (%q, %q)", sum, errMsg)
	}
}

func TestHealedStepCount(t *testing.T) {
	if n := healedStepCount([]byte(`[{"action":"navigate","url":"/"},{"action":"click","selector":"#a"}]`)); n != 2 {
		t.Fatalf("healedStepCount = %d, want 2", n)
	}
	if n := healedStepCount([]byte(`not json`)); n != 0 {
		t.Fatalf("healedStepCount(bad) = %d, want 0", n)
	}
}

func TestHealInfoSummary(t *testing.T) {
	h := &HealInfo{
		TriggerStepSummary: "wait_visible [data-testid='tool-call-completed']",
		TriggerError:       "timeout after 60s waiting for: ...",
		TriggerWasTimeout:  true,
		RetriesBeforeHeal:  2,
		AgentTurns:         3,
		AgentToolCalls:     4,
		StepsChanged:       true,
		CachedStepCount:    10,
		HealedStepCount:    10,
	}
	s := h.Summary()
	for _, want := range []string{"transient timeout", "tool-call-completed", "retries before heal: 2", "3 turns, 4 tool calls", "steps changed: yes"} {
		if !strings.Contains(s, want) {
			t.Errorf("Summary() = %q, missing %q", s, want)
		}
	}
	h.AgentHitMaxTurns = true
	if !strings.Contains(h.Summary(), "hit max turns") {
		t.Errorf("Summary() should flag hit max turns: %q", h.Summary())
	}
}

func TestSummaryIncludesHeal(t *testing.T) {
	results := []*TestResult{{
		Description:  "healed test",
		Pass:         true,
		Mode:         RunModeHealed,
		CacheVersion: 2,
		Heal: &HealInfo{
			TriggerStepSummary: "wait_visible #x",
			TriggerError:       "timeout after 60s",
			TriggerWasTimeout:  true,
			AgentTurns:         2,
			StepsChanged:       false,
		},
	}}
	dir := t.TempDir()
	path := filepath.Join(dir, "summary.json")
	if err := WriteSummary(path, results); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s RunSummary
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Tests) != 1 || s.Tests[0].Heal == nil {
		t.Fatal("expected heal info in summary JSON")
	}
	if s.Tests[0].Heal.AgentTurns != 2 || !s.Tests[0].Heal.TriggerWasTimeout {
		t.Fatalf("heal fields not serialized: %+v", s.Tests[0].Heal)
	}
	// A non-healed test must omit the heal block.
	if !strings.Contains(string(data), "\"heal\"") {
		t.Error("expected heal key present for healed test")
	}
}

func TestReportRendersHeal(t *testing.T) {
	results := []*TestResult{{
		Description:  "healed test",
		Pass:         true,
		Mode:         RunModeHealed,
		CacheVersion: 2,
		Heal: &HealInfo{
			TriggerStepSummary: "wait_visible #x",
			TriggerError:       "timeout after 60s waiting for: #x",
			TriggerWasTimeout:  true,
			RetriesBeforeHeal:  2,
			AgentTurns:         2,
			AgentToolCalls:     3,
			StepsChanged:       false,
			CachedStepCount:    5,
			HealedStepCount:    5,
		},
	}}
	dir := t.TempDir()
	if err := WriteReport(dir, results); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.html"))
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)
	for _, want := range []string{"Healed", "transient timeout", "wait_visible #x", "Retries before heal"} {
		if !strings.Contains(html, want) {
			t.Errorf("report HTML missing %q", want)
		}
	}
}
