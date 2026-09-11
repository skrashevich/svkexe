package picoclaw

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/runtime"
)

const (
	agentDBWaitInterval     = 500 * time.Millisecond
	agentDBWaitTimeout      = 60 * time.Second
	guestSystemdWaitTimeout = 90 * time.Second
	agentStartTimeout       = 60 * time.Second
	agentStartInterval      = 2 * time.Second
)

// SeedLLMModels inserts gateway proxy models into PicoClaw's SQLite database.
// PicoClaw must be running and have initialized its database.
func SeedLLMModels(ctx context.Context, rt runtime.ContainerRuntime, incusName string, llmCfg *LLMProxyConfig) error {
	if llmCfg == nil || llmCfg.BaseURL == "" || len(llmCfg.Models) == 0 {
		log.Printf("picoclaw: skip model seed for %s (llm config missing)", incusName)
		return nil
	}
	log.Printf("picoclaw: seeding %d models into %s", len(llmCfg.Models), incusName)

	if err := waitForPicoClawDB(ctx, rt, incusName); err != nil {
		return err
	}

	seedSQL := buildSeedSQL(llmCfg.Models, llmCfg.BaseURL, llmCfg.Token)
	sqlPath := ConfigDir + "/models.sql"
	if err := writeGuestFile(ctx, rt, incusName, sqlPath, []byte(seedSQL)); err != nil {
		return err
	}
	apply := fmt.Sprintf("sqlite3 -bail %s < %s; result=$?; rm -f %s; exit $result", DBPath, sqlPath, sqlPath)
	if _, err := rt.Exec(ctx, incusName, []string{"sh", "-c", apply}); err != nil {
		return fmt.Errorf("seed models: %w", err)
	}

	wantIDs := expectedSeedModelIDs(llmCfg.Models)
	gotIDs, err := readSeededModelIDs(ctx, rt, incusName)
	if err != nil {
		return fmt.Errorf("verify seeded models: %w", err)
	}
	if !slices.Equal(gotIDs, wantIDs) {
		return fmt.Errorf("seeded model IDs mismatch: got %v, want %v", gotIDs, wantIDs)
	}
	log.Printf("picoclaw: seeded %d models into %s", len(gotIDs), incusName)
	return nil
}

func waitForGuestSystemd(ctx context.Context, rt runtime.ContainerRuntime, incusName string) error {
	// Degraded is ready too: an unrelated failed guest unit must not block us.
	ctx, cancel := context.WithTimeout(ctx, guestSystemdWaitTimeout)
	defer cancel()
	check := "state=$(systemctl is-system-running 2>/dev/null); [ \"$state\" = running ] || [ \"$state\" = degraded ]"
	deadline := time.Now().Add(guestSystemdWaitTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := rt.Exec(ctx, incusName, []string{"sh", "-c", check}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentDBWaitInterval):
		}
	}
	return fmt.Errorf("timed out waiting for guest systemd in %s", incusName)
}

func startPicoClawService(ctx context.Context, rt runtime.ContainerRuntime, incusName string) error {
	if _, err := rt.Exec(ctx, incusName, []string{"systemctl", "enable", "picoclaw.service"}); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	deadline := time.Now().Add(agentStartTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, _ = rt.Exec(ctx, incusName, []string{"systemctl", "start", "picoclaw.service"})
		out, err := rt.Exec(ctx, incusName, []string{"systemctl", "is-active", "picoclaw.service"})
		if err == nil && strings.TrimSpace(string(out)) == "active" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentStartInterval):
		}
	}
	return fmt.Errorf("picoclaw.service did not become active in %s", incusName)
}

func waitForPicoClawDB(ctx context.Context, rt runtime.ContainerRuntime, incusName string) error {
	check := fmt.Sprintf(
		"test -f %s && sqlite3 %s \"SELECT 1 FROM sqlite_master WHERE type='table' AND name='models' LIMIT 1;\"",
		DBPath, DBPath,
	)
	deadline := time.Now().Add(agentDBWaitTimeout)
	for time.Now().Before(deadline) {
		if err := ctx.Err(); err != nil {
			return err
		}
		out, err := rt.Exec(ctx, incusName, []string{"sh", "-c", check})
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(agentDBWaitInterval):
		}
	}
	return fmt.Errorf("timed out waiting for PicoClaw database at %s", DBPath)
}

func buildSeedSQL(models []string, baseURL, token string) string {
	stmts := make([]string, 0, len(models)+3)
	stmts = append(stmts, "PRAGMA busy_timeout=10000;", "BEGIN IMMEDIATE;")
	stmts = append(stmts, "DELETE FROM models WHERE model_id LIKE '"+gatewayModelPrefix+"%';")
	for _, model := range models {
		stmts = append(stmts, modelSeedStmt(model, baseURL, token))
	}
	stmts = append(stmts, "COMMIT;")
	return strings.Join(stmts, "\n")
}

func expectedSeedModelIDs(models []string) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		ids = append(ids, gatewayModelPrefix+model)
	}
	slices.Sort(ids)
	return ids
}

// listGuestModels returns every model ID the VM's agent database holds.
//
// Everything that picks a model for a VM asks this rather than reasoning from
// what the gateway believes it seeded. The two drift: the deployment-wide model
// list can change after a VM was set up, a refresh re-seeds the owner's models
// but not the gateway's, and naming a model the agent database lacks leaves the
// VM unable to answer at all.
func listGuestModels(ctx context.Context, rt runtime.ContainerRuntime, incusName string) ([]string, error) {
	out, err := rt.Exec(ctx, incusName, []string{"sqlite3", DBPath, "PRAGMA busy_timeout=10000; SELECT model_id FROM models ORDER BY model_id;"})
	if err != nil {
		return nil, fmt.Errorf("list models in %s: %w", incusName, err)
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func readSeededModelIDs(ctx context.Context, rt runtime.ContainerRuntime, incusName string) ([]string, error) {
	verifyCmd := []string{"sqlite3", DBPath, "PRAGMA busy_timeout=10000; SELECT model_id FROM models WHERE model_id LIKE '" + gatewayModelPrefix + "%' ORDER BY model_id;"}
	out, err := rt.Exec(ctx, incusName, verifyCmd)
	if err != nil {
		return nil, fmt.Errorf("query model IDs: %w; diagnostics: %s", err, collectSeedDiagnostics(ctx, rt, incusName))
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return []string{}, nil
	}
	lines := strings.Split(text, "\n")
	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		id := strings.TrimSpace(line)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func collectSeedDiagnostics(ctx context.Context, rt runtime.ContainerRuntime, incusName string) string {
	parts := []string{}

	dbCmd := []string{"sh", "-c", fmt.Sprintf("if [ -f %s ]; then echo present; else echo missing; fi", DBPath)}
	if out, err := rt.Exec(ctx, incusName, dbCmd); err != nil {
		parts = append(parts, fmt.Sprintf("db_file=unknown (%v)", err))
	} else {
		parts = append(parts, "db_file="+strings.TrimSpace(string(out)))
	}

	tablesCmd := []string{"sh", "-c", fmt.Sprintf("sqlite3 %s \"SELECT name FROM sqlite_master WHERE type='table' ORDER BY name;\" 2>/dev/null", DBPath)}
	if out, err := rt.Exec(ctx, incusName, tablesCmd); err != nil {
		parts = append(parts, fmt.Sprintf("tables=unavailable (%v)", err))
	} else {
		tables := strings.TrimSpace(string(out))
		if tables == "" {
			tables = "<none>"
		}
		parts = append(parts, "tables="+strings.ReplaceAll(tables, "\n", ","))
	}

	modelsTableCmd := []string{"sh", "-c", fmt.Sprintf("sqlite3 %s \"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='models';\" 2>/dev/null", DBPath)}
	if out, err := rt.Exec(ctx, incusName, modelsTableCmd); err != nil {
		parts = append(parts, fmt.Sprintf("models_table=unknown (%v)", err))
	} else {
		val := strings.TrimSpace(string(out))
		switch val {
		case "1":
			parts = append(parts, "models_table=present")
		case "0":
			parts = append(parts, "models_table=missing")
		default:
			parts = append(parts, "models_table=unknown ("+val+")")
		}
	}

	return strings.Join(parts, "; ")
}

func modelSeedStmt(model, baseURL, token string) string {
	displayName := model
	if idx := strings.LastIndex(model, "/"); idx >= 0 {
		displayName = model[idx+1:]
	}
	modelID := gatewayModelPrefix + model
	return fmt.Sprintf(
		"INSERT OR REPLACE INTO models (model_id, display_name, provider_type, endpoint, api_key, model_name, max_tokens) VALUES (%s, %s, 'openai', %s, %s, %s, 200000);",
		sqlQuote(modelID), sqlQuote(displayName), sqlQuote(baseURL), sqlQuote(token), sqlQuote(model),
	)
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// A live process alone does not establish that the agent UI/API is ready.
func waitForAgentHTTP(ctx context.Context, rt runtime.ContainerRuntime, name string) error {
	ctx, cancel := context.WithTimeout(ctx, agentStartTimeout)
	defer cancel()
	for {
		if _, err := rt.Exec(ctx, name, []string{"curl", "--fail", "--silent", "--max-time", "3", "-H", RequireHeader + ": setup", "http://127.0.0.1:9000/api/models"}); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("PicoClaw HTTP readiness: %w", ctx.Err())
		case <-time.After(agentStartInterval):
		}
	}
}
