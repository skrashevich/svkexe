package picoclaw

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
	"github.com/skrashevich/svkexe/internal/secrets"
)

type guestRuntime struct {
	runtime.ContainerRuntime
	commands []string
	files    map[string][]byte
	fail     string
	// models is what the VM's agent database already held before this test, one
	// model ID per line. The SQL the gateway applies then mutates it.
	models string
	// dbModels is that table as it stands now; dbLoaded guards its one-time
	// initialisation and seeded records that the gateway has applied model SQL.
	dbModels []string
	dbLoaded bool
	seeded   bool
	// newConversation overrides the agent's reply to a posted task.
	newConversation string
	// progress is what the agent's database reports for a task conversation,
	// in the "agent_working|last message type" form the query returns.
	progress string
	// agentError is the text stored on the task's last error message.
	agentError string
	// failedTurn is what the agent's database reports about the task's latest
	// failure, in the "retryable|seconds since it happened" form the query
	// returns. Empty means a fresh failure the agent called permanent.
	failedTurn string
	// resumeStatus is the HTTP status the agent answers a resume request with.
	// Empty means it accepts the task back.
	resumeStatus string
	// resumes counts the resume requests the agent received.
	resumes int
	// taskLookup is the conversation the agent's database reports for a task
	// whose conversation the gateway never recorded.
	taskLookup string
	// metadataRouteStamp is the route version this VM already carries. Empty
	// means it carries none, which is a VM the gateway has not configured yet.
	metadataRouteStamp string
}

func (g *guestRuntime) Exec(_ context.Context, _ string, cmd []string) ([]byte, error) {
	text := strings.Join(cmd, " ")
	g.commands = append(g.commands, text)
	if g.fail != "" && strings.Contains(text, g.fail) {
		return nil, errors.New("guest command failed")
	}
	if strings.Contains(text, "sqlite3 -bail") && strings.Contains(text, " < ") {
		_, rest, _ := strings.Cut(text, " < ")
		path, _, _ := strings.Cut(rest, ";")
		g.applyModelSQL(string(g.files[strings.TrimSpace(path)]))
	}
	if len(cmd) == 3 && cmd[0] == "sh" && strings.Contains(cmd[2], " | base64 -d > ") {
		encoded := strings.Split(cmd[2], "'")[3]
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, err
		}
		path := strings.Split(cmd[2], " > ")[1]
		g.files[path] = data
	}
	switch {
	case len(cmd) == 2 && cmd[0] == "cat":
		return g.files[cmd[1]], nil
	case strings.HasPrefix(text, "sh -c cat "):
		// The real command redirects stderr and forces a zero exit, so a
		// missing file is empty output rather than an error.
		path, _, _ := strings.Cut(strings.TrimPrefix(text, "sh -c cat "), " ")
		return g.files[path], nil
	case len(cmd) == 2 && cmd[1] == "version":
		return []byte(`{"version":"picoclaw-v0.3.1-svkexe","customized":true}`), nil
	case strings.Contains(text, "is-active"):
		return []byte("active\n"), nil
	case strings.Contains(text, "SELECT 1 FROM sqlite_master"):
		return []byte("1\n"), nil
	case strings.Contains(text, "SELECT model_id"):
		return g.listModels(text), nil
	case strings.Contains(text, "/api/conversations/new"):
		if g.newConversation != "" {
			return []byte(g.newConversation), nil
		}
		return []byte(`{"status":"accepted","conversation_id":"cTASK01"}`), nil
	case strings.Contains(text, "agent_working"):
		// The query is expected to look past a trailing slug marker, exactly as
		// the agent does; a fake that answered regardless would hide the very
		// bug this guards.
		if !strings.Contains(text, "type != 'slug'") {
			return nil, errors.New("progress query does not skip slug messages")
		}
		return []byte(g.progress + "\n"), nil
	case strings.Contains(text, "json_extract(user_data"):
		if g.failedTurn != "" {
			return []byte(g.failedTurn + "\n"), nil
		}
		return []byte("0|0\n"), nil
	case strings.Contains(text, "/retry"):
		g.resumes++
		if g.resumeStatus != "" {
			return []byte(g.resumeStatus), nil
		}
		return []byte("202"), nil
	case strings.Contains(text, "CAST(x'"):
		return []byte(g.taskLookup + "\n"), nil
	case strings.Contains(text, "json_extract(llm_data"):
		return []byte(g.agentError + "\n"), nil
	case strings.Contains(text, metadataRouteStamp) && strings.Contains(text, "is-enabled"):
		// The metadata route probe. A guest that already carries this exact
		// version answers successfully; anything else is a miss, which is what
		// makes the gateway reinstall.
		if g.metadataRouteStamp != "" && strings.Contains(text, g.metadataRouteStamp) {
			return nil, nil
		}
		return nil, errors.New("route not installed")
	}
	return nil, nil
}

// modelsInVM is the agent database's models table as the fake keeps it.
func (g *guestRuntime) modelsInVM() []string {
	if !g.dbLoaded {
		g.dbLoaded = true
		for _, line := range strings.Split(g.models, "\n") {
			if id := strings.TrimSpace(line); id != "" {
				g.dbModels = append(g.dbModels, id)
			}
		}
	}
	return g.dbModels
}

// applyModelSQL mirrors what sqlite3 does to the models table, DELETEs
// included. Those deletes are what keep a retired model from lingering, and a
// fake that only replayed the INSERTs could not show a mis-scoped one — nor
// could it show a VM ending up on a model the seeding never created, which is
// the whole reason the model choice is now made from this list.
func (g *guestRuntime) applyModelSQL(sql string) {
	g.modelsInVM()
	g.seeded = true
	for _, stmt := range strings.Split(sql, "\n") {
		switch {
		case strings.HasPrefix(stmt, "DELETE FROM models WHERE model_id GLOB '"),
			strings.HasPrefix(stmt, "DELETE FROM models WHERE model_id LIKE '"):
			_, rest, _ := strings.Cut(stmt, "model_id ")
			_, pattern, _ := strings.Cut(rest, "'")
			pattern, _, _ = strings.Cut(pattern, "'")
			prefix := strings.TrimRight(pattern, "*%")
			g.dbModels = slices.DeleteFunc(g.dbModels, func(id string) bool {
				return strings.HasPrefix(id, prefix)
			})
		case strings.HasPrefix(stmt, "INSERT"):
			_, values, found := strings.Cut(stmt, "VALUES ('")
			if !found {
				continue
			}
			id, _, _ := strings.Cut(values, "'")
			if id != "" && !slices.Contains(g.dbModels, id) {
				g.dbModels = append(g.dbModels, id)
			}
		}
	}
}

// listModels answers a model-listing query from that table.
func (g *guestRuntime) listModels(query string) []byte {
	ids := slices.Clone(g.modelsInVM())
	if !g.seeded && g.models == "" {
		// A VM set up on an earlier run, whose inventory this test does not
		// bother to describe. Setting models to whitespace is how a test says
		// the VM genuinely holds nothing, as against saying nothing about it.
		ids = []string{"svkexe-test/model"}
	}
	if strings.Contains(query, "LIKE 'svkexe-%'") {
		ids = slices.DeleteFunc(ids, func(id string) bool {
			return !strings.HasPrefix(id, gatewayModelPrefix)
		})
	}
	slices.Sort(ids)
	if len(ids) == 0 {
		return nil
	}
	return []byte(strings.Join(ids, "\n") + "\n")
}

func (g *guestRuntime) PushFile(_ context.Context, _, path string, data []byte) error {
	g.files[path] = data
	return nil
}
func (g *guestRuntime) PullFile(context.Context, string, string) ([]byte, error) {
	return []byte("archive"), nil
}

// The gateway list is deployment-wide and can name models an account cannot
// reach, so a VM whose owner has their own key must open on that key instead.
func TestSetupPrefersOwnerModelOverGatewayList(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	enc := []byte("01234567890123456789012345678901")
	if err := database.SaveProviderKey("key", owner.ID, "openrouter", "secret", "https://host/api/v1", "openrouter/free", "", enc); err != nil {
		t.Fatal(err)
	}
	m := secrets.NewMaterializer(database, enc, t.TempDir())
	guest := &guestRuntime{files: map[string][]byte{}}
	cfg := &LLMProxyConfig{BaseURL: "http://gateway/api/llm/v1", Token: "token", Models: []string{"cohere/north-mini-code:free"}}

	if err := SetupContainer(t.Context(), guest, nil, m, testContainer(owner.ID), cfg); err != nil {
		t.Fatal(err)
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	if config["default_model"] != "svkexe_user:openrouter:openrouter/free" {
		t.Fatalf("default model = %q, want the owner's own model", config["default_model"])
	}
	// The gateway models are still seeded, only demoted to a fallback.
	if !strings.Contains(string(guest.files[ConfigDir+"/models.sql"]), "svkexe-cohere/north-mini-code:free") {
		t.Error("gateway models no longer available in the VM")
	}
}

func TestSetupMigrationAndGatewayConfiguration(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	guest := &guestRuntime{files: map[string][]byte{EnvFilePath: []byte("OLD_KEY=stale")}}
	cfg := &LLMProxyConfig{BaseURL: "http://gateway/api/llm/v1", Token: "token'$(not-a-command)\nSEED_EOF", Models: []string{"test/model"}}
	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), cfg); err != nil {
		t.Fatal(err)
	}
	if string(guest.files["/usr/local/bin/picoclaw.new"]) != "agent-binary" {
		t.Fatal("agent artifact not installed")
	}
	var config map[string]string
	if err := json.Unmarshal(guest.files[ConfigFilePath], &config); err != nil {
		t.Fatal(err)
	}
	if config["default_model"] != "svkexe-test/model" || config["llm_gateway"] != "" {
		t.Fatalf("wrong model routing: %v", config)
	}
	if len(guest.files[EnvFilePath]) != 0 {
		t.Fatal("deleted credentials persisted")
	}
	script := strings.Join(guest.commands, "\n")
	if strings.Contains(script, cfg.Token) {
		t.Fatal("token interpolated into shell")
	}
	for _, want := range []string{
		"/data/picoclaw.pre-rename.db",
		"mv " + LegacyDBPath + " " + DBPath,
		"rm -f /usr/local/bin/shelley /etc/systemd/system/shelley.service",
		"systemctl daemon-reload",
		"restart picoclaw.service",
		"http://127.0.0.1:9000/api/models",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("missing migration step %s", want)
		}
	}
	if strings.Contains(script, LegacyConfigDir+"/env "+LegacyConfigDir) {
		t.Error("legacy config directory removed before its credentials were moved")
	}
	if !strings.Contains(string(guest.files[ConfigDir+"/models.sql"]), sqlQuote(cfg.Token)) {
		t.Fatal("gateway token not seeded losslessly")
	}
	if strings.Index(script, "disable --now") > strings.Index(script, "enable picoclaw.service") {
		t.Fatal("old agent stopped after new agent started")
	}
}

func TestSetupRefusesMissingArtifact(t *testing.T) {
	t.Setenv("SVKEXE_AGENT_BINARY", filepath.Join(t.TempDir(), "missing"))
	guest := &guestRuntime{files: map[string][]byte{}}
	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err == nil {
		t.Fatal("missing agent reported ready")
	}
	for _, cmd := range guest.commands {
		if strings.Contains(cmd, "disable --now") {
			t.Fatal("old service touched before artifact validation")
		}
	}
}
func TestBackupFailureIsReturned(t *testing.T) {
	guest := &guestRuntime{fail: "tar -czf"}
	if _, err := BackupData(t.Context(), guest, "vm"); err == nil {
		t.Fatal("backup failure ignored")
	}
}
func TestSetupCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := SetupContainer(ctx, &guestRuntime{}, nil, nil, testContainer("owner"), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestSetupRejectsIncompatibleArtifactBeforeStopping(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
	guest := &guestRuntime{files: map[string][]byte{}, fail: "picoclaw.new version"}
	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err == nil {
		t.Fatal("incompatible guest binary accepted")
	}
	for _, cmd := range guest.commands {
		if strings.Contains(cmd, "disable --now") {
			t.Fatal("working agent stopped before new binary validation")
		}
	}
}
