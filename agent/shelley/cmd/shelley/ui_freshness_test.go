package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestUIFreshnessScopedToServerAssets(t *testing.T) {
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}

	fixtureDir := t.TempDir()
	srcDir := filepath.Join(fixtureDir, "src")
	if err := os.Mkdir(srcDir, 0o755); err != nil {
		t.Fatalf("create fixture source directory: %v", err)
	}
	srcFile := filepath.Join(srcDir, "fixture.ts")
	if err := os.WriteFile(srcFile, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("write fixture source: %v", err)
	}

	buildTime := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	buildInfo, err := json.Marshal(map[string]any{
		"timestamp": buildTime.UnixMilli(),
		"date":      "fixture build",
		"srcDir":    srcDir,
	})
	if err != nil {
		t.Fatalf("marshal fixture build info: %v", err)
	}
	buildInfoPath := filepath.Join(fixtureDir, "build-info.json")
	if err := os.WriteFile(buildInfoPath, buildInfo, 0o644); err != nil {
		t.Fatalf("write fixture build info: %v", err)
	}
	indexPath := filepath.Join(fixtureDir, "index.html")
	if err := os.WriteFile(indexPath, []byte("<!doctype html><title>fixture</title>"), 0o644); err != nil {
		t.Fatalf("write fixture index: %v", err)
	}

	overlay, err := json.Marshal(map[string]any{
		"Replace": map[string]string{
			filepath.Join(repoRoot, "ui", "dist", "build-info.json"): buildInfoPath,
			filepath.Join(repoRoot, "ui", "dist", "index.html"):      indexPath,
		},
	})
	if err != nil {
		t.Fatalf("marshal Go build overlay: %v", err)
	}
	overlayPath := filepath.Join(fixtureDir, "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o644); err != nil {
		t.Fatalf("write Go build overlay: %v", err)
	}

	binary := filepath.Join(fixtureDir, "shelley")
	build := exec.Command("go", "build", "-overlay", overlayPath, "-o", binary, "./cmd/shelley")
	build.Dir = repoRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Shelley with fixture assets: %v\n%s", err, output)
	}

	homeDir := filepath.Join(fixtureDir, "home")
	workDir := filepath.Join(fixtureDir, "work")
	for _, dir := range []string{homeDir, workDir} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatalf("create isolated CLI directory: %v", err)
		}
	}
	env := isolatedCLIEnv(homeDir)

	t.Run("stale assets", func(t *testing.T) {
		setFixtureModTime(t, srcFile, buildTime.Add(time.Minute))
		assertUnrelatedCLICommandsWork(t, binary, workDir, env)
		assertStaleServeFails(t, binary, fixtureDir, workDir, env, srcFile)
	})

	t.Run("fresh assets", func(t *testing.T) {
		setFixtureModTime(t, srcFile, buildTime.Add(-time.Minute))
		assertUnrelatedCLICommandsWork(t, binary, workDir, env)
		assertFreshServeStarts(t, binary, fixtureDir, workDir, env)
	})
}

func isolatedCLIEnv(homeDir string) []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "HOME=") ||
			strings.HasPrefix(item, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(item, "LISTEN_PID=") ||
			strings.HasPrefix(item, "LISTEN_FDS=") {
			continue
		}
		env = append(env, item)
	}
	return append(
		env,
		"HOME="+homeDir,
		"XDG_CONFIG_HOME="+filepath.Join(homeDir, ".config"),
	)
}

func setFixtureModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("set fixture source mtime: %v", err)
	}
}

func assertUnrelatedCLICommandsWork(t *testing.T, binary, workDir string, env []string) {
	t.Helper()

	version := exec.Command(binary, "version")
	version.Dir = workDir
	version.Env = env
	output, err := version.CombinedOutput()
	if err != nil {
		t.Fatalf("version failed: %v\n%s", err, output)
	}
	var info map[string]any
	if err := json.Unmarshal(output, &info); err != nil {
		t.Fatalf("version returned invalid JSON: %v\n%s", err, output)
	}
	if _, ok := info["version"]; !ok {
		t.Fatalf("version output omitted version: %s", output)
	}
	if bytes.Contains(output, []byte("UI build is stale")) {
		t.Fatalf("version unexpectedly checked UI freshness: %s", output)
	}

	skill := exec.Command(binary, "skill", "cat", "schedule")
	skill.Dir = workDir
	skill.Env = env
	output, err = skill.CombinedOutput()
	if err != nil {
		t.Fatalf("skill cat failed: %v\n%s", err, output)
	}
	if !bytes.Contains(output, []byte("name: schedule")) {
		t.Fatalf("skill cat returned unexpected content: %s", output)
	}
	if bytes.Contains(output, []byte("UI build is stale")) {
		t.Fatalf("skill cat unexpectedly checked UI freshness: %s", output)
	}
}

func assertStaleServeFails(t *testing.T, binary, fixtureDir, workDir string, env []string, srcFile string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(
		ctx, binary,
		"-db", filepath.Join(fixtureDir, "stale.db"),
		"-predictable-only",
		"-disable-llm-integration",
		"-disable-gateway",
		"serve",
		"-port", "0",
		"-socket", "none",
	)
	cmd.Dir = workDir
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("serve did not reject stale assets before timeout:\n%s", output)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != 1 {
		t.Fatalf("serve with stale assets exited with %v, want status 1:\n%s", err, output)
	}
	for _, want := range []string{
		"Error: UI build is stale!",
		"Build timestamp: fixture build",
		srcFile,
		"Please run 'make serve' instead of 'go run ./cmd/shelley serve'",
		"Or rebuild the UI first: cd ui && pnpm run build",
	} {
		if !bytes.Contains(output, []byte(want)) {
			t.Errorf("stale serve output missing %q:\n%s", want, output)
		}
	}
}

func assertFreshServeStarts(t *testing.T, binary, fixtureDir, workDir string, env []string) {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("create inherited listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	file, err := listener.(*net.TCPListener).File()
	if err != nil {
		listener.Close()
		t.Fatalf("duplicate inherited listener: %v", err)
	}
	if err := listener.Close(); err != nil {
		file.Close()
		t.Fatalf("close original listener: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(
		ctx, binary,
		"-db", filepath.Join(fixtureDir, "fresh.db"),
		"-predictable-only",
		"-disable-llm-integration",
		"-disable-gateway",
		"serve",
		"-systemd-activation",
		"-socket", "none",
	)
	cmd.Dir = workDir
	cmd.Env = append(append([]string{}, env...), "LISTEN_FDS=1")
	cmd.ExtraFiles = []*os.File{file}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		file.Close()
		t.Fatalf("start serve with fresh assets: %v", err)
	}
	if err := file.Close(); err != nil {
		cancel()
		_ = cmd.Wait()
		t.Fatalf("close inherited listener after start: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		cancel()
		_ = cmd.Wait()
		stopped = true
	}
	defer stop()

	client := &http.Client{Timeout: 20 * time.Second}
	response, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/version", port))
	if err != nil {
		stop()
		t.Fatalf("fresh server did not answer: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		stop()
		t.Fatalf("read fresh server response: %v", err)
	}
	stop()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("fresh server returned %d: %s\nstderr: %s", response.StatusCode, body, stderr.String())
	}
	if bytes.Contains(stderr.Bytes(), []byte("UI build is stale")) {
		t.Fatalf("fresh server unexpectedly rejected UI assets:\n%s", stderr.String())
	}
}
