package picoclaw

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

const (
	// refreshLockWait bounds waiting for a VM that is mid-setup or mid-delivery.
	// It has to cover the longest of those, which is task delivery.
	refreshLockWait = taskDeliveryTimeout + 15*time.Second

	// refreshWork bounds the refresh itself once the VM is ours.
	refreshWork = 30 * time.Second
)

func providerModelsSQL(models []secrets.ProviderModel) string {
	var sql strings.Builder
	fmt.Fprintf(&sql, "PRAGMA busy_timeout=10000;\nBEGIN IMMEDIATE;\nDELETE FROM models WHERE model_id GLOB '%s*';\n", db.UserModelPrefix)
	for _, m := range models {
		fmt.Fprintf(&sql, "INSERT INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES (%s, %s, %s, %s, %s, %s, 200000);\n",
			sqlQuote(m.ID()), sqlQuote(m.Provider+" / "+m.Model), sqlQuote(m.Protocol), sqlQuote(m.BaseURL), sqlQuote(m.Key), sqlQuote(m.Model))
	}
	sql.WriteString("COMMIT;\n")
	return sql.String()
}

func seedProviderModels(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel) error {
	sqlPath := ConfigDir + "/provider-models.sql"
	if err := writeGuestFile(ctx, rt, name, sqlPath, []byte(providerModelsSQL(models))); err != nil {
		return err
	}
	apply := fmt.Sprintf("sqlite3 -bail %s < %s; result=$?; rm -f %s; exit $result", DBPath, sqlPath, sqlPath)
	_, err := rt.Exec(ctx, name, []string{"sh", "-c", apply})
	return err
}

// providerModelIDs are the agent-side IDs of the owner's own models, in the
// order their settings list them.
func providerModelIDs(models []secrets.ProviderModel) []string {
	ids := make([]string, 0, len(models))
	for _, m := range models {
		ids = append(ids, m.ID())
	}
	return ids
}

// gatewaySeeded reports whether the gateway is the one that put this model in
// the VM. Anything else is the owner's own creation inside the agent — an
// Ollama entry they added themselves — which the gateway does not get to move
// them off.
//
// The test is the ID prefix, which the agent does not reserve. A model the owner
// creates under one of these prefixes — by cloning a platform entry in the
// agent's UI, say — is therefore read as the gateway's: it loses this protection
// AND the next platform seed deletes it, since that seed clears the prefix
// wholesale. Closing this properly means recording provenance at seed time
// rather than inferring it, and having the delete target that record.
func gatewaySeeded(id string) bool {
	return strings.HasPrefix(id, db.UserModelPrefix) || strings.HasPrefix(id, gatewayModelPrefix)
}

// gatewayModelIDs are the deployment's models as agent-side IDs, in the order
// the operator listed them. That order is a preference rather than an accident:
// the LLM proxy tries the models in it, so a VM opening on a different one puts
// the two out of step.
func gatewayModelIDs(llmCfg *LLMProxyConfig) []string {
	if llmCfg == nil {
		return nil
	}
	ids := make([]string, 0, len(llmCfg.Models))
	for _, model := range llmCfg.Models {
		ids = append(ids, gatewayModelPrefix+model)
	}
	return ids
}

// desiredModel picks the model a VM should open on, choosing only from the
// models it actually has.
//
//	available — every model ID in the VM's agent database
//	own       — the owner's own model IDs, in the order their settings list them
//	platform  — the deployment's model IDs, in the operator's order; may be nil
//	            where that order is not known, and is filtered by availability
//	            either way
//	chosen    — the model the owner picked in their LLM settings, empty for Auto
//	current   — the model the VM names today
//
// The owner's own models replace the platform's: that list is deployment-wide
// and can name models this account cannot reach, while a connection the owner
// configured is one they chose and pay for. Among their own, whatever the VM
// already names is their in-agent preference and stands — but a platform model
// does not get that protection, or adding a first connection would leave every
// running VM on the platform's key.
func desiredModel(available, own, platform []string, chosen, current string) string {
	reachable := func(id string) bool { return id != "" && slices.Contains(available, id) }
	if reachable(chosen) {
		return chosen
	}
	if reachable(current) && !gatewaySeeded(current) {
		return current
	}
	var mine []string
	for _, id := range own {
		if reachable(id) {
			mine = append(mine, id)
		}
	}
	if len(mine) > 0 {
		if slices.Contains(mine, current) {
			return current
		}
		return mine[0]
	}
	// Nothing of their own to move them to, so whatever the VM runs on keeps
	// working.
	if reachable(current) {
		return current
	}
	for _, id := range platform {
		if reachable(id) {
			return id
		}
	}
	if gateway := firstWithPrefix(available, gatewayModelPrefix); gateway != "" {
		return gateway
	}
	if len(available) > 0 {
		return available[0]
	}
	return ""
}

// readGuestConfig returns the agent configuration as it stands in the VM.
//
// Values are kept as raw JSON rather than decoded. The gateway only ever reads
// and writes one string setting, but the agent keeps numbers, booleans and
// objects here too, and decoding those into strings does not fail cleanly —
// encoding/json records the type error and stores the empty string, so a
// round-trip would silently rewrite `"max_tokens": 8192` as `"max_tokens": ""`
// and leave the agent unable to parse its own configuration.
//
// Do not reach for map[string]any instead: it decodes numbers as float64, so a
// large integer would come back rewritten in exponent form.
//
// A missing or unparseable file reads as empty; a file that could not be read at
// all is an error. The distinction matters because the caller rewrites what it
// gets back: a corrupt file is one the agent cannot read either, so replacing it
// loses nothing that still worked, while an exec failure says nothing about the
// file and proceeding on an empty map would overwrite a perfectly good config.
// Redirecting stderr and forcing a zero exit is what separates them — a missing
// file becomes empty output rather than the non-zero exit `cat` would otherwise
// give, which the runtime reports indistinguishably from a failed exec.
//
// A parse failure is retried once. The likeliest cause is not corruption but a
// torn read: the agent rewrites this file when the owner switches model in its
// UI, and truncate-then-write leaves a window where the file is half there. That
// window closes in milliseconds, so a second read either parses or confirms the
// file really is broken.
func readGuestConfig(ctx context.Context, rt runtime.ContainerRuntime, name string) (map[string]json.RawMessage, error) {
	read := func() ([]byte, error) {
		return rt.Exec(ctx, name, []string{"sh", "-c", "cat " + ConfigFilePath + " 2>/dev/null || true"})
	}
	for attempt := range 2 {
		out, err := read()
		if err != nil {
			return nil, fmt.Errorf("read agent config in %s: %w", name, err)
		}
		cfg := map[string]json.RawMessage{}
		if len(bytes.TrimSpace(out)) == 0 {
			return cfg, nil
		}
		if err := json.Unmarshal(out, &cfg); err == nil {
			return cfg, nil
		}
		if attempt == 0 {
			select {
			case <-time.After(100 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	log.Printf("picoclaw: agent config in %s is not valid JSON; replacing it", name)
	return map[string]json.RawMessage{}, nil
}

// guestDefaultModelOf returns the default_model cfg names, or "" when it names
// none or names something that is not a string.
//
// The empty answer therefore covers four different files — the key absent, set
// to "", set to null, or set to a number — so callers deciding whether to
// rewrite must ask guestNamesDefaultModel as well. Only one of those four is
// the state the gateway leaves behind, and the other three are values the agent
// cannot use.
func guestDefaultModelOf(cfg map[string]json.RawMessage) string {
	var current string
	if err := json.Unmarshal(cfg["default_model"], &current); err != nil {
		return ""
	}
	return current
}

// guestNamesDefaultModel reports whether the setting is present at all.
func guestNamesDefaultModel(cfg map[string]json.RawMessage) bool {
	_, present := cfg["default_model"]
	return present
}

// writeGuestConfig stores cfg with default_model set to value, or removed when
// value is empty. Every other setting is carried through untouched.
func writeGuestConfig(ctx context.Context, rt runtime.ContainerRuntime, name string, cfg map[string]json.RawMessage, value string) error {
	if value == "" {
		delete(cfg, "default_model")
	} else {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		cfg["default_model"] = encoded
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	return writeGuestFile(ctx, rt, name, ConfigFilePath, data)
}

// applyDefaultModel points a VM at the model its owner should be on, leaving the
// file untouched when it already names it. It must run after the models have
// been seeded, since it chooses from what the VM actually has.
func applyDefaultModel(ctx context.Context, rt runtime.ContainerRuntime, name string, models []secrets.ProviderModel, chosen string, llmCfg *LLMProxyConfig) error {
	available, err := listGuestModels(ctx, rt, name)
	if err != nil {
		return err
	}
	cfg, err := readGuestConfig(ctx, rt, name)
	if err != nil {
		return err
	}
	current := guestDefaultModelOf(cfg)
	want := desiredModel(available, providerModelIDs(models), gatewayModelIDs(llmCfg), chosen, current)
	// Skipping the write when nothing is wanted is only safe once the setting is
	// actually gone. A file naming "" or null or a number decodes to the same
	// empty current, and leaving one of those in place would keep the agent on a
	// value it cannot use — which is exactly the state this is meant to clear
	// when the VM has no models at all.
	if want == current && (want != "" || !guestNamesDefaultModel(cfg)) {
		return nil
	}
	return writeGuestConfig(ctx, rt, name, cfg, want)
}

// RefreshProviderKeys applies provider changes to the running agent, including
// deletion, key rotation and a change of default model. Restart reloads both env
// credentials and DB models.
//
// chosen is the owner's selected default model, empty when they have not picked
// one. llmCfg carries the operator's model order, which decides the fallback
// when the owner has none of their own; it may be nil.
func RefreshProviderKeys(ctx context.Context, rt runtime.ContainerRuntime, m *secrets.Materializer, id, name, owner, chosen string, llmCfg *LLMProxyConfig) error {
	// Waiting for the lock gets its own budget, separate from the work. Setup
	// and task delivery hold it for longer than this refresh needs to run, so
	// sharing one deadline would report a settings change as failed to sync
	// purely because a VM was mid-setup — and tell the owner to restart a VM
	// that was about to pick the change up anyway.
	waitCtx, cancelWait := context.WithTimeout(ctx, refreshLockWait)
	defer cancelWait()
	lock, _ := setupLocks.LoadOrStore(name, make(chan struct{}, 1))
	gate := lock.(chan struct{})
	select {
	case gate <- struct{}{}:
		defer func() { <-gate }()
	case <-waitCtx.Done():
		return fmt.Errorf("sync LLM settings to %s: %w", name, waitCtx.Err())
	}
	ctx, cancel := context.WithTimeout(ctx, refreshWork)
	defer cancel()
	if err := m.RefreshKeys(id, owner); err != nil {
		return err
	}
	env, err := m.ReadKeys(id)
	if err != nil {
		return err
	}
	if err := writeGuestFile(ctx, rt, name, EnvFilePath, env); err != nil {
		return err
	}
	models, err := m.ProviderModels(owner)
	if err != nil {
		return err
	}
	if err := seedProviderModels(ctx, rt, name, models); err != nil {
		return err
	}
	if err := applyDefaultModel(ctx, rt, name, models, chosen, llmCfg); err != nil {
		return err
	}
	protect := fmt.Sprintf("chown root:user %[1]s %[2]s && chmod 640 %[1]s %[2]s && systemctl restart picoclaw.service", EnvFilePath, ConfigFilePath)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", protect}); err != nil {
		return err
	}
	return waitForAgentHTTP(ctx, rt, name)
}
