package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"shelley.exe.dev/claudetool"
	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/llm"
	"shelley.exe.dev/llm/predictable"
)

// withReflectionStatus swaps in a fake reflection client that replies with the
// given status and body, restoring the original client on cleanup.
func withReflectionStatus(t *testing.T, status int, body string) {
	t.Helper()
	env, err := exeenv.Current()
	if err != nil {
		t.Fatal(err)
	}
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old); resetReflectionStateCache() })
	resetReflectionStateCache()
	// A reflection 403 now triggers a direct llm.int probe (discovery falls
	// back to it), so answer that too. Default: also absent, i.e. the real
	// production shape where both integrations are detached.
	llmURL := env.IntegrationURL("llm", false) + "/models.json"
	setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == llmURL {
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Body:       io.NopCloser(strings.NewReader("")),
				Header:     make(http.Header),
			}, nil
		}
		if req.URL.String() != env.ReflectionURL()+"/integrations" {
			t.Fatalf("unexpected reflection URL %s", req.URL)
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})})
}

// TestModelSetupHintNotOnExeDev: off exe.dev there are no integrations to
// blame, so the hint must stay generic ("add a model yourself").
func TestModelSetupHintNotOnExeDev(t *testing.T) {
	if got := modelSetupHintIn(context.Background(), false); got != modelSetupHintLocal {
		t.Fatalf("modelSetupHint off exe.dev = %q, want %q", got, modelSetupHintLocal)
	}
}

// TestModelSetupHintMissingReflection: reflection AND the direct llm probe
// both return 403 when the integrations have been removed or detached. This is
// the exact state that produced "Unsupported model: claude-sonnet-4.6" in
// production, reproduced on a real VM. Reflection alone being down is not
// enough (see TestReflectionDownButLLMServing) because discovery falls back to
// llm.int, so the report names both.
func TestModelSetupHintMissingReflection(t *testing.T) {
	withReflectionStatus(t, http.StatusForbidden, "integration not found or not attached to this VM")
	if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintMissingBoth {
		t.Fatalf("modelSetupHint with reflection+llm 403 = %q, want %q", got, modelSetupHintMissingBoth)
	}
}

// TestModelSetupHintMissingLLM: reflection works but exposes no "llm"
// integration, so there is nothing for Shelley to draw models from.
func TestModelSetupHintMissingLLM(t *testing.T) {
	withReflectionStatus(t, http.StatusOK, `{"integrations":[{"name":"reflection","type":"reflection"}]}`)
	if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintMissingLLM {
		t.Fatalf("modelSetupHint without llm integration = %q, want %q", got, modelSetupHintMissingLLM)
	}
}

// TestModelSetupHintTransientFailure: a reflection outage (timeout, 5xx,
// garbage body) is NOT evidence that the integration is missing. Telling the
// user to mutate their integrations because of a blip is bad advice, so
// transient failures must classify as unknown.
func TestModelSetupHintTransientFailure(t *testing.T) {
	for _, tt := range []struct {
		name   string
		status int
		body   string
	}{
		{"server error", http.StatusInternalServerError, "boom"},
		{"bad gateway", http.StatusBadGateway, "boom"},
		{"malformed body", http.StatusOK, "not json"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			withReflectionStatus(t, tt.status, tt.body)
			if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintUnknown {
				t.Fatalf("modelSetupHint on %s = %q, want %q", tt.name, got, modelSetupHintUnknown)
			}
		})
	}
}

// TestModelSetupHintBothPresent: both integrations are healthy, so the
// empty model list has some other cause and we must not misdirect the user
// toward integrations that are already configured correctly.
func TestModelSetupHintBothPresent(t *testing.T) {
	withReflectionStatus(t, http.StatusOK,
		`{"integrations":[{"name":"reflection","type":"reflection"},{"name":"llm","type":"llm"}]}`)
	if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintUnknown {
		t.Fatalf("modelSetupHint with both integrations = %q, want %q", got, modelSetupHintUnknown)
	}
}

// TestModelSetupHintOnlyWhenNoModels guards the cost/behavior contract: the
// hint (and its reflection probe) is for the broken empty-list case only. A
// server with models must not advertise setup help at all.
func TestModelSetupHintOnlyWhenNoModels(t *testing.T) {
	if hint := modelSetupHintForModels(context.Background(), []ModelInfo{{ID: "predictable", Ready: true}}, true); hint != "" {
		t.Fatalf("modelSetupHint with models present = %q, want empty", hint)
	}
	withReflectionStatus(t, http.StatusForbidden, "nope")
	if hint := modelSetupHintForModels(context.Background(), nil, true); hint != modelSetupHintMissingBoth {
		t.Fatalf("modelSetupHint with no models = %q, want %q", hint, modelSetupHintMissingBoth)
	}
}

// emptyLLMManager serves no models at all — the production state that
// produced the bogus "Unsupported model: claude-sonnet-4.6" error. GetService
// must fail for every id, like the real manager does with an empty catalog;
// testLLMManager happily returns a service for any id, which would silently
// hide exactly the 400s these tests are about.
type emptyLLMManager struct{ testLLMManager }

func (m *emptyLLMManager) GetAvailableModels() []string { return nil }
func (m *emptyLLMManager) HasModel(string) bool         { return false }
func (m *emptyLLMManager) GetService(modelID string) (llm.Service, error) {
	return nil, fmt.Errorf("unsupported model: %s", modelID)
}

// TestIndexInitDataCarriesModelSetupHint is the end-to-end contract the UI
// relies on: when the served model list is empty, index.html must tell the UI
// why, so it can print an actionable message instead of inventing a model id.
func TestIndexInitDataCarriesModelSetupHint(t *testing.T) {
	withReflectionStatus(t, http.StatusForbidden, "integration not found or not attached to this VM")
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &emptyLLMManager{testLLMManager{service: ps}},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "", "")
	srv.hooksDir = t.TempDir()

	init := indexInitData(t, srv)
	if models, ok := init["models"].([]any); !ok || len(models) != 0 {
		t.Fatalf("expected empty models in init data, got %#v", init["models"])
	}
	if got := init["default_model"]; got != "" {
		t.Fatalf("default_model = %#v, want empty (no models to default to)", got)
	}
	// isExeDev() is environment-dependent, so accept either exe.dev-specific
	// or local copy — but a reason MUST be present, and it must never be a
	// model id the UI could try to send.
	switch got := init["model_setup_hint"]; got {
	case modelSetupHintMissingBoth, modelSetupHintMissingReflection, modelSetupHintLocal, modelSetupHintUnknown:
	default:
		t.Fatalf("model_setup_hint = %#v, want a known setup-hint token", got)
	}
}

// TestIndexInitDataCarriesExeDevFlag: the UI needs to know it is on exe.dev
// independently of the hint token. The token is only emitted when the catalog
// is empty at page load, so if the catalog empties LATER (integration
// detached, then Refresh) the UI has no token and would otherwise fall back to
// the off-exe.dev copy — telling an exe.dev user to add a model by hand
// instead of naming the integration that broke.
func TestIndexInitDataCarriesExeDevFlag(t *testing.T) {
	srv, _, _ := newTestServer(t)
	init := indexInitData(t, srv)
	got, ok := init["is_exe_dev"]
	if !ok {
		t.Fatal("init data must always carry is_exe_dev")
	}
	if want := isExeDev(); got != want {
		t.Fatalf("is_exe_dev = %#v, want %#v", got, want)
	}
}

func TestIndexInitDataCarriesUserEmail(t *testing.T) {
	srv, _, _ := newTestServer(t)
	init := indexInitDataWithEmail(t, srv, " alice@example.com ")
	if got := init["user_email"]; got != "alice@example.com" {
		t.Fatalf("user_email = %#v, want alice@example.com", got)
	}
}

// TestIndexInitDataOmitsHintWithModels: a healthy server must not ship setup
// advice (and must not pay for a reflection probe on every page load).
func TestIndexInitDataOmitsHintWithModels(t *testing.T) {
	srv, _, _ := newTestServer(t)
	init := indexInitData(t, srv)
	if models, ok := init["models"].([]any); !ok || len(models) == 0 {
		t.Fatalf("expected models in init data, got %#v", init["models"])
	}
	if got, ok := init["model_setup_hint"]; ok {
		t.Fatalf("model_setup_hint = %#v, want absent when models exist", got)
	}
}

// indexInitData fetches "/" and parses the injected window.__SHELLEY_INIT__.
func indexInitData(t *testing.T, srv *Server) map[string]any {
	return indexInitDataWithEmail(t, srv, "")
}

func indexInitDataWithEmail(t *testing.T, srv *Server, email string) map[string]any {
	t.Helper()
	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("X-ExeDev-Email", email)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", w.Code)
	}
	const marker = "window.__SHELLEY_INIT__="
	body := w.Body.String()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatal("no window.__SHELLEY_INIT__ in index.html")
	}
	dec := json.NewDecoder(strings.NewReader(body[i+len(marker):]))
	var init map[string]any
	if err := dec.Decode(&init); err != nil {
		t.Fatalf("decode init data: %v", err)
	}
	return init
}

// TestUnsupportedModelMessageWhenNoModels: when the server serves NO models,
// naming the rejected id is actively misleading — every client (web, iOS,
// exed prompt paths, curl) that lands here got "Unsupported model: X" for an
// id the user never chose. The message must say the real problem instead.
// This covers the paths the web send-guard cannot: a non-draft conversation's
// persisted model, queued messages, retry, and non-browser clients.
func TestUnsupportedModelMessageWhenNoModels(t *testing.T) {
	// Pin BOTH environments explicitly. isExeDev() stats /exe.dev, so calling
	// through it would silently test only the host's own branch — green on a
	// dev VM, and it was exactly the off-exe.dev branch that broke CI.
	for _, tc := range []struct {
		name     string
		onExeDev bool
		wants    []string
	}{
		{"on exe.dev", true, []string{"llm", "reflection"}},
		{"off exe.dev", false, []string{"model picker"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := unsupportedModelMessageIn("claude-sonnet-4.6", nil, tc.onExeDev)
			if strings.Contains(got, "claude-sonnet-4.6") {
				t.Errorf("message must not name the model when none are configured: %q", got)
			}
			if !strings.Contains(got, "No AI models") {
				t.Errorf("message should explain that no models are configured: %q", got)
			}
			// Keep it short: this is an HTTP error body surfaced in a status
			// bar and in non-browser clients, not a place for a tutorial.
			if len(got) > 160 {
				t.Errorf("message is %d chars, want < 160: %q", len(got), got)
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("message should mention %q: %q", want, got)
				}
			}
		})
	}
}

// TestUnsupportedModelMessageWithModels: with models available, a bad id is a
// genuine client error and naming it is the useful thing to do.
func TestUnsupportedModelMessageWithModels(t *testing.T) {
	got := unsupportedModelMessage("nope-9000", []ModelInfo{{ID: "claude-opus-4.8", Ready: true}})
	if !strings.Contains(got, "nope-9000") {
		t.Errorf("message should name the unsupported model: %q", got)
	}
	if strings.Contains(got, "reflection") {
		t.Errorf("message must not give setup advice when models exist: %q", got)
	}
}

// TestReflectionProbeCachedAndCollapsed: the probe sits on the index.html
// request path, so a hung reflection endpoint must not cost every page load
// (and a reloading user must not pile up probes). One probe should serve
// concurrent and closely-spaced callers.
func TestReflectionProbeCachedAndCollapsed(t *testing.T) {
	env, err := exeenv.Current()
	if err != nil {
		t.Fatal(err)
	}
	// Count DIAGNOSES, not raw requests: one diagnosis of the both-detached
	// state legitimately makes two calls (reflection, then the direct llm
	// fallback), so the reflection hit is what identifies a fresh probe.
	var probes atomic.Int64
	release := make(chan struct{})
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old); resetReflectionStateCache() })
	resetReflectionStateCache()
	reflectionURL := env.ReflectionURL() + "/integrations"
	setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() == reflectionURL {
			probes.Add(1)
			<-release // hold the probe open so the racers must coalesce
		}
		return &http.Response{
			StatusCode: http.StatusForbidden,
			Body:       io.NopCloser(strings.NewReader("nope")),
			Header:     make(http.Header),
		}, nil
	})})

	const racers = 8
	var wg sync.WaitGroup
	// arrived counts racers that have entered cachedReflectionState. Waiting
	// on it (rather than sleeping) keeps this deterministic: we only release
	// the probe once every racer is committed, so any extra probe would have
	// to show up in the count.
	arrived := make(chan struct{}, racers)
	got := make([]reflectionState, racers)
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			arrived <- struct{}{}
			got[i] = cachedReflectionState(context.Background(), env)
		}()
	}
	for range racers {
		<-arrived
	}
	// The first racer's probe is blocked in the transport; the rest are either
	// waiting on singleflight or about to. Release and let them all resolve.
	close(release)
	wg.Wait()

	if n := probes.Load(); n != 1 {
		t.Errorf("concurrent callers made %d probes, want 1 (singleflight)", n)
	}
	for i, st := range got {
		if st != reflectionUnavailable {
			t.Errorf("racer %d got %v, want reflectionUnavailable", i, st)
		}
	}

	// A later caller inside the TTL must reuse the cached answer.
	if st := cachedReflectionState(context.Background(), env); st != reflectionUnavailable {
		t.Errorf("cached read got %v, want reflectionUnavailable", st)
	}
	if n := probes.Load(); n != 1 {
		t.Errorf("cached read made an extra probe (total %d), want 1", n)
	}

	// Once the TTL lapses the diagnosis must be re-probed, so that a user who
	// fixes their integrations sees recovery without restarting Shelley.
	resetReflectionStateCache()
	if st := cachedReflectionState(context.Background(), env); st != reflectionUnavailable {
		t.Errorf("re-probe got %v, want reflectionUnavailable", st)
	}
	if n := probes.Load(); n != 2 {
		t.Errorf("expired cache made %d total probes, want 2", n)
	}
}

// TestCreateDraftWithNoModels: a draft is autosaved composer text, not a turn.
// Rejecting it because no model is configured loses what the user typed AND
// wedges draftAutosave in a retry loop ("Draft autosave failed; will retry")
// while they are off fixing their integrations. The model is validated later,
// when the draft is promoted by an actual send.
func TestCreateDraftWithNoModels(t *testing.T) {
	withReflectionStatus(t, http.StatusForbidden, "nope")
	database, cleanup := setupTestDB(t)
	t.Cleanup(cleanup)
	ps := predictable.NewService()
	srv := NewServer(database, &emptyLLMManager{testLLMManager{service: ps}},
		claudetool.ToolSetConfig{EnableBrowser: false},
		slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelWarn})),
		false, "", "")
	srv.hooksDir = t.TempDir()

	mux := http.NewServeMux()
	srv.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, "/api/conversations/draft",
		strings.NewReader(`{"draft":"half-typed thought"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusCreated {
		t.Fatalf("POST /api/conversations/draft = %d (%s), want 201", w.Code, strings.TrimSpace(w.Body.String()))
	}
}

// withReflectionAndLLM stubs both probe endpoints independently so a test can
// describe the real production shapes (e.g. reflection 403 + llm.int 200).
func withReflectionAndLLM(t *testing.T, env exeenv.Environment, reflectionStatus int, reflectionBody string, llmStatus int, llmBody string) {
	t.Helper()
	old := reflectionHTTPClient()
	t.Cleanup(func() { setReflectionHTTPClient(old); resetReflectionStateCache() })
	resetReflectionStateCache()
	reflectionURL := env.ReflectionURL() + "/integrations"
	llmURL := env.IntegrationURL("llm", false) + "/models.json"
	setReflectionHTTPClient(&http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		status, body := 0, ""
		switch req.URL.String() {
		case reflectionURL:
			status, body = reflectionStatus, reflectionBody
		case llmURL:
			status, body = llmStatus, llmBody
		default:
			t.Errorf("unexpected probe URL %s", req.URL)
			status, body = http.StatusNotFound, ""
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     make(http.Header),
		}, nil
	})})
}

const llmCatalogWithModels = `{"schema_version":1,"models":[{"id":"openai/gpt-5.6-sol","provider":"openai","native_id":"gpt-5.6-sol","apis":["openai_responses"]}]}`

// TestReflectionDownButLLMServing: reflection 403 while llm.int serves a
// catalog. Verified on a real VM (llm attached, reflection detached): Shelley
// still served 126 models, because DiscoverLLMIntegrations falls back to the
// default personal "llm" integration when reflection fails
// (modelsources.go:505). So reflection being down is NOT the blocker and must
// not be reported as the thing to fix.
func TestReflectionDownButLLMServing(t *testing.T) {
	env := exeenv.FromHostname("box.exe.xyz")
	withReflectionAndLLM(t, env, http.StatusForbidden, "", http.StatusOK, llmCatalogWithModels)
	if got := modelSetupHintIn(context.Background(), true); got == modelSetupHintMissingLLM {
		t.Fatalf("hint = %q, but llm.int is serving models; must not blame the llm integration", got)
	}
	if got := cachedReflectionState(context.Background(), env); got != reflectionLLMReachable {
		t.Fatalf("state = %v, want reflectionLLMReachable", got)
	}
}

// TestBothIntegrationsDetached is the actual production failure. Reproduced on
// a real VM by detaching llm (reflection was already detached): both endpoints
// 403 and /api/models returned []. The minimal fix that restores models is
// attaching llm, so llm must lead the remedy — reflection alone would not have
// helped.
func TestBothIntegrationsDetached(t *testing.T) {
	env := exeenv.FromHostname("box.exe.xyz")
	withReflectionAndLLM(t, env, http.StatusForbidden, "", http.StatusForbidden, "")
	if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintMissingBoth {
		t.Fatalf("hint with both detached = %q, want %q", got, modelSetupHintMissingBoth)
	}
}

// TestLLMTransientFailureIsNotDiagnosed: a 5xx or timeout from llm.int is not
// evidence that the integration is missing. Never send a user to mutate
// working integrations because of a blip.
func TestLLMTransientFailureIsNotDiagnosed(t *testing.T) {
	env := exeenv.FromHostname("box.exe.xyz")
	withReflectionAndLLM(t, env, http.StatusForbidden, "", http.StatusInternalServerError, "")
	if got := modelSetupHintIn(context.Background(), true); got != modelSetupHintUnknown {
		t.Fatalf("hint with llm 500 = %q, want %q", got, modelSetupHintUnknown)
	}
}

// TestLLMCatalogWithNoServeableModels: the llm integration answers 200 with a
// well-formed catalog whose models Shelley cannot serve (unknown api type).
// Discovery filters those out (integrationModelsFromCatalog), so it yields
// zero models — meaning llm.int is NOT a working source. The diagnosis must
// agree with discovery rather than judging the catalog by its own weaker
// rules, otherwise it reports "llm is fine" about a catalog that produces
// nothing.
func TestLLMCatalogWithNoServeableModels(t *testing.T) {
	env := exeenv.FromHostname("box.exe.xyz")
	const unserveable = `{"schema_version":1,"models":[{"id":"weird/model","provider":"weird","native_id":"weird","apis":["telepathy"]}]}`
	withReflectionAndLLM(t, env, http.StatusForbidden, "", http.StatusOK, unserveable)
	if got := cachedReflectionState(context.Background(), env); got == reflectionLLMReachable {
		t.Fatal("state = reflectionLLMReachable, but no catalog model is serveable by Shelley")
	}
}
