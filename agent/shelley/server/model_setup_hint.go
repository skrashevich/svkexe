package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"shelley.exe.dev/exeenv"
	"shelley.exe.dev/modelsources"
)

// maxLLMCatalogBytes caps the diagnosis read. Real catalogs are ~100KB; this
// probe runs on a page load, so a hostile or broken endpoint must not be able
// to stream unbounded data into it.
const maxLLMCatalogBytes = 4 << 20

// unsupportedModelMessage builds the 400 body for a model the server can't
// serve. When the catalog is EMPTY the id is beside the point: every client
// (web, iOS, exed prompt paths, curl) otherwise gets "Unsupported model: X"
// naming an id the user never chose — the confusion this whole change exists
// to remove. The web UI blocks most of these locally, but it cannot cover a
// non-draft conversation's persisted model, queued messages, retries, or
// non-browser clients, so the server has to explain itself too.
func unsupportedModelMessage(modelID string, modelList []ModelInfo) string {
	return unsupportedModelMessageIn(modelID, modelList, isExeDev())
}

// unsupportedModelMessageIn is the testable core: isExeDev() stats /exe.dev,
// so a test that calls through it only ever exercises whichever branch its
// host happens to be on (dev VMs are on exe.dev, CI is not).
func unsupportedModelMessageIn(modelID string, modelList []ModelInfo, onExeDev bool) string {
	if len(modelList) > 0 {
		return fmt.Sprintf("Unsupported model: %s", modelID)
	}
	if !onExeDev {
		return "No AI models are configured. Add one in Shelley's model picker."
	}
	// Name the llm integration first: it is what actually serves models, and
	// attaching it alone is enough (discovery probes llm.int directly when
	// reflection is unavailable). Kept short — the web UI shows clickable
	// remedies; this body also reaches curl and mobile clients where prose is
	// all we have.
	return "No AI models are configured. On exe.dev, attach the llm " +
		"integration (and reflection) to this VM, then refresh the model list."
}

// Machine-readable reasons for an empty model list. The UI maps these to
// localized copy; keeping them as stable tokens (rather than prose) means
// translations live in the UI's i18n bundles like every other string.
const (
	// modelSetupHintLocal: not an exe.dev VM, so there are no exe.dev
	// integrations to configure. The user must add a model themselves.
	modelSetupHintLocal = "add_model"
	// modelSetupHintMissingReflection: the reflection integration is missing
	// or detached. Shelley discovers LLM integrations *through* reflection,
	// so this masks the LLM integration too and must be fixed first.
	modelSetupHintMissingReflection = "exe_reflection_missing"
	// modelSetupHintMissingLLM: reflection works but exposes no "llm"
	// integration, so there is no model source to draw from.
	modelSetupHintMissingLLM = "exe_llm_missing"
	// modelSetupHintMissingBoth: neither reflection nor the default llm
	// integration is reachable. This is the production failure. Attaching llm
	// alone restores models (verified on a real VM), so the UI leads with llm
	// and offers reflection as the secondary fix.
	modelSetupHintMissingBoth = "exe_both_missing"
	// modelSetupHintUnknown: both integrations look healthy, so the empty
	// list has some other cause. Don't misdirect the user toward
	// integrations that are already correct.
	modelSetupHintUnknown = "exe_unknown"
)

// modelSetupHintForModels returns the reason the model list is empty, or ""
// when models are present. Only called with an empty list in practice; the
// guard keeps the reflection probe off the healthy path.
func modelSetupHintForModels(ctx context.Context, modelList []ModelInfo, onExeDev bool) string {
	if len(modelList) > 0 {
		return ""
	}
	return modelSetupHintIn(ctx, onExeDev)
}

// modelSetupHintIn diagnoses an empty model list. Off exe.dev the answer is
// static; on exe.dev it probes reflection to distinguish "reflection is
// missing" from "reflection is fine but no llm integration is attached",
// because the remedies are different commands.
func modelSetupHintIn(ctx context.Context, onExeDev bool) string {
	if !onExeDev {
		return modelSetupHintLocal
	}
	env, err := exeenv.Current()
	if err != nil {
		return modelSetupHintUnknown
	}
	switch cachedReflectionState(ctx, env) {
	case reflectionUnavailable:
		// Reflection is down AND the direct llm probe failed too.
		return modelSetupHintMissingBoth
	case reflectionLLMReachable:
		// Reflection is down but llm.int serves a catalog, which discovery
		// falls back to. The llm integration is fine, so don't blame it; an
		// empty list here has some other cause.
		return modelSetupHintUnknown
	case reflectionWithoutLLM:
		return modelSetupHintMissingLLM
	default:
		return modelSetupHintUnknown
	}
}

const (
	// reflectionProbeTimeout bounds one probe. The page is already broken when
	// we get here, so a short wait to explain why is worth it — but it must not
	// be long enough to look like a hang.
	reflectionProbeTimeout = 2 * time.Second
	// reflectionStateTTL caches the diagnosis briefly. Deliberately NOT a
	// sync.Once (cf. exeNotifyAvailable): the whole point is that the user goes
	// and fixes their integrations, and the fixed state has to become visible
	// on reload without restarting Shelley.
	reflectionStateTTL = 10 * time.Second
)

var (
	reflectionStateMu   sync.Mutex
	reflectionStateVal  reflectionState
	reflectionStateAt   time.Time
	reflectionStateFly  singleflight.Group
	reflectionStateOnce bool
)

// cachedReflectionState probes at most once per reflectionStateTTL and
// collapses concurrent probes. Without this, a slow or hung reflection
// endpoint would add up to reflectionProbeTimeout to *every* page load of a
// broken VM, and a reloading user (or several tabs) would pile probes up.
func cachedReflectionState(ctx context.Context, env exeenv.Environment) reflectionState {
	reflectionStateMu.Lock()
	if reflectionStateOnce && time.Since(reflectionStateAt) < reflectionStateTTL {
		v := reflectionStateVal
		reflectionStateMu.Unlock()
		return v
	}
	reflectionStateMu.Unlock()

	// Use a detached context for the shared probe: the winner's result is
	// shared, so one client navigating away must not cancel the lookup for
	// everyone else waiting on it.
	v, _, _ := reflectionStateFly.Do("reflection", func() (any, error) {
		st := reflectionIntegrationState(context.WithoutCancel(ctx), env)
		reflectionStateMu.Lock()
		reflectionStateVal, reflectionStateAt, reflectionStateOnce = st, time.Now(), true
		reflectionStateMu.Unlock()
		return st, nil
	})
	state, ok := v.(reflectionState)
	if !ok {
		return reflectionUnknown
	}
	return state
}

type reflectionState int

const (
	// reflectionUnavailable is an AUTHORITATIVE negative: exed says this VM
	// has neither reflection nor a directly-reachable llm integration
	// (403/404 from both). Removed and detached both land here and share one
	// remedy path, so they share one state.
	reflectionUnavailable reflectionState = iota
	// reflectionUnknown is a non-answer: timeout, 5xx, unparseable body. NOT
	// evidence of misconfiguration, so it must not send the user off to mutate
	// integrations that may be perfectly fine.
	reflectionUnknown
	reflectionWithoutLLM
	reflectionWithLLM
	// reflectionLLMReachable: reflection is unreachable but the default
	// personal llm integration serves a catalog. DiscoverLLMIntegrations
	// falls back to exactly this (modelsources.go:505), so models still work
	// and the llm integration must not be reported as missing.
	reflectionLLMReachable
)

// reflectionIntegrationState probes the reflection integration to see whether
// it is reachable and whether it exposes an "llm" integration.
func reflectionIntegrationState(ctx context.Context, env exeenv.Environment) reflectionState {
	ctx, cancel := context.WithTimeout(ctx, reflectionProbeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", env.ReflectionURL()+"/integrations", nil)
	if err != nil {
		return reflectionUnknown
	}
	resp, err := reflectionHTTPClient().Do(req)
	if err != nil {
		return reflectionUnknown
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		// exed's authoritative "not found or not attached to this VM". Before
		// blaming reflection, check whether the default llm integration is
		// reachable on its own — discovery falls back to it, so models may be
		// working fine without reflection.
		return llmIntegrationFallbackState(ctx, env)
	case resp.StatusCode != http.StatusOK:
		return reflectionUnknown
	}
	var body struct {
		Integrations []reflectionIntegration `json:"integrations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return reflectionUnknown
	}
	for _, ig := range body.Integrations {
		if ig.Type == "llm" {
			return reflectionWithLLM
		}
	}
	return reflectionWithoutLLM
}

// llmIntegrationFallbackState probes the default personal "llm" integration
// directly, mirroring DiscoverLLMIntegrations' fallback when reflection fails
// (modelsources.go:505). Without this, a VM with llm attached but reflection
// detached gets told to fix reflection even though its models work.
func llmIntegrationFallbackState(ctx context.Context, env exeenv.Environment) reflectionState {
	req, err := http.NewRequestWithContext(ctx, "GET", env.IntegrationURL("llm", false)+"/models.json", nil)
	if err != nil {
		return reflectionUnknown
	}
	resp, err := reflectionHTTPClient().Do(req)
	if err != nil {
		return reflectionUnknown
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusForbidden, resp.StatusCode == http.StatusNotFound:
		// Both sources are authoritatively absent: the real production case.
		return reflectionUnavailable
	case resp.StatusCode != http.StatusOK:
		// A blip is not a diagnosis.
		return reflectionUnknown
	}
	// Only a catalog that yields models Shelley can actually serve proves this
	// source works. Judged by discovery's own filter (a catalog can be valid
	// JSON yet contain nothing usable), so the diagnosis can't disagree with
	// what discovery would do. Anything else leaves the cause open rather than
	// misdirecting the user.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxLLMCatalogBytes))
	if err != nil {
		return reflectionUnknown
	}
	if !modelsources.CatalogHasServeableModels(body) {
		return reflectionUnknown
	}
	return reflectionLLMReachable
}

// resetReflectionStateCache clears the cached diagnosis. Used by tests, which
// swap the reflection client per case and would otherwise observe a previous
// case's cached answer.
func resetReflectionStateCache() {
	reflectionStateMu.Lock()
	reflectionStateOnce = false
	reflectionStateVal = reflectionUnknown
	reflectionStateAt = time.Time{}
	reflectionStateMu.Unlock()
}
