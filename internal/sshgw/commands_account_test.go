package sshgw

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	gssh "github.com/gliderlabs/ssh"
	"github.com/skrashevich/svkexe/internal/db"
	gossh "golang.org/x/crypto/ssh"
)

// theSecret is the credential every LLM test stores. It is a single distinctive
// string so that any output which leaks it fails a test rather than being read
// past.
const theSecret = "sk-secret-that-must-never-be-printed-0123456789"

// accountServer builds a gateway with a real database and no runtime: every
// command in this group is a database operation, and a nil runtime is also what
// makes the VM sync a no-op, so the assertions are about what was stored.
func accountServer(t *testing.T) (*Server, *db.User) {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	user := &db.User{ID: "u", Email: "owner@example.test", Role: "user", CreatedAt: time.Now()}
	if err := database.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	return &Server{db: database, encKey: bytes.Repeat([]byte{7}, 32)}, user
}

// authorizedKey generates a public key in the form a caller would paste in.
func authorizedKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := gossh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(gossh.MarshalAuthorizedKey(signer)))
}

// keySession answers PublicKey the way a real authenticated session does, which
// is what lets a test reach the guard on removing the key you are holding.
type keySession struct {
	gssh.Session
	key gssh.PublicKey
}

func (s *keySession) PublicKey() gssh.PublicKey { return s.key }

// TestSSHLLMConnectionLifecycle walks a connection from stored to listed to
// deleted, and holds the whole way that the credential is never handed back.
func TestSSHLLMConnectionLifecycle(t *testing.T) {
	s, user := accountServer(t)

	out, err := run(t, s, user, "llm add custom-lab "+theSecret+" --base-url=https://lab.example/v1 --models=gpt-5,gpt-5-mini")
	if err != nil {
		t.Fatalf("llm add: %v (%s)", err, out)
	}
	keys, err := s.db.ListAPIKeysByOwner(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || keys[0].Provider != "custom-lab" || keys[0].BaseURL != "https://lab.example/v1" || keys[0].Models != "gpt-5,gpt-5-mini" {
		t.Fatalf("llm add stored %+v", keys)
	}
	// The protocol is not given on the command line, so the stored row has to
	// carry the default rather than an empty string the agent cannot act on.
	if keys[0].Protocol != db.DefaultProtocol {
		t.Errorf("stored protocol %q, want %q", keys[0].Protocol, db.DefaultProtocol)
	}
	if plain, err := s.db.GetAPIKeyPlaintext(keys[0].ID, s.encKey); err != nil || plain != theSecret {
		t.Fatalf("stored key %q, %v", plain, err)
	}

	text, err := run(t, s, user, "llm list")
	if err != nil {
		t.Fatalf("llm list: %v", err)
	}
	if !strings.Contains(text, "custom-lab") || !strings.Contains(text, "https://lab.example/v1") || !strings.Contains(text, "gpt-5-mini") {
		t.Errorf("llm list omitted the connection:\n%s", text)
	}
	if strings.Contains(text, theSecret) {
		t.Errorf("llm list printed the credential:\n%s", text)
	}

	raw, err := run(t, s, user, "llm list --json")
	if err != nil {
		t.Fatalf("llm list --json: %v", err)
	}
	if strings.Contains(raw, theSecret) {
		t.Errorf("llm list --json returned the credential:\n%s", raw)
	}
	var connections []llmConnectionJSON
	if err := json.Unmarshal([]byte(raw), &connections); err != nil {
		t.Fatalf("llm list --json is not JSON: %v (%s)", err, raw)
	}
	if len(connections) != 1 || connections[0].Provider != "custom-lab" || len(connections[0].Models) != 2 {
		t.Fatalf("llm list --json returned %+v", connections)
	}

	if out, err := run(t, s, user, "llm rm custom-lab"); err != nil {
		t.Fatalf("llm rm: %v (%s)", err, out)
	}
	keys, err = s.db.ListAPIKeysByOwner(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Fatalf("llm rm left %d connections", len(keys))
	}
	if _, err := run(t, s, user, "llm rm custom-lab"); err == nil {
		t.Error("removing a connection that is gone was accepted")
	}
}

// TestSSHLLMAddRejectsIncompleteSettings holds that the shell surfaces
// NormalizeProvider's own text, which is the only place that explains which
// half of a combination is missing.
func TestSSHLLMAddRejectsIncompleteSettings(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"unknown provider", "llm add notaprovider " + theSecret, "invalid provider"},
		{"custom without endpoint", "llm add custom-lab " + theSecret, "requires a base URL"},
		{"endpoint without models", "llm add custom-lab " + theSecret + " --base-url=https://lab.example/v1", "at least one model"},
		{"provider key missing", "llm add openai", "key is required"},
		// openrouter is given its own base URL when none is typed, and an
		// endpoint that names no models leaves nothing to point a VM at.
		{"openrouter without models", "llm add openrouter " + theSecret, "at least one model"},
		{"unknown protocol", "llm add custom-lab " + theSecret + " --base-url=https://lab.example/v1 --models=a --protocol=grpc", "protocol must be one of"},
		{"no provider named", "llm add", "name the provider"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s, user := accountServer(t)
			_, err := run(t, s, user, tc.line)
			if err == nil {
				t.Fatalf("%q was accepted", tc.line)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain %q", err, tc.want)
			}
			keys, _ := s.db.ListAPIKeysByOwner(user.ID)
			if len(keys) != 0 {
				t.Errorf("a rejected connection was stored anyway: %+v", keys)
			}
		})
	}
}

// TestSSHLLMDefaultModel covers the three answers the choice can get: a model
// the owner reaches, one they do not, and handing the choice back.
func TestSSHLLMDefaultModel(t *testing.T) {
	s, user := accountServer(t)
	if _, err := run(t, s, user, "llm add custom-lab "+theSecret+" --base-url=https://lab.example/v1 --models=gpt-5"); err != nil {
		t.Fatal(err)
	}
	reachable := db.UserModelID("custom-lab", "gpt-5")

	// A model with no connection behind it is the caller's mistake, and the
	// refusal has to name what they could have picked instead.
	_, err := run(t, s, user, "llm default svkexe_user:custom-lab:gpt-4")
	if err == nil {
		t.Fatal("an unreachable model was accepted")
	}
	if !strings.Contains(err.Error(), "unknown model") || !strings.Contains(err.Error(), reachable) {
		t.Errorf("refusal %q does not name the choices", err)
	}
	if chosen, _ := s.db.UserDefaultModel(user.ID); chosen != "" {
		t.Errorf("the rejected model was stored as %q", chosen)
	}

	if out, err := run(t, s, user, "llm default "+reachable); err != nil {
		t.Fatalf("llm default: %v (%s)", err, out)
	}
	if chosen, _ := s.db.UserDefaultModel(user.ID); chosen != reachable {
		t.Fatalf("stored default %q, want %q", chosen, reachable)
	}

	models, err := run(t, s, user, "llm models --json")
	if err != nil {
		t.Fatalf("llm models --json: %v", err)
	}
	var view llmModelsJSON
	if err := json.Unmarshal([]byte(models), &view); err != nil {
		t.Fatalf("llm models --json is not JSON: %v (%s)", err, models)
	}
	if view.Default != reachable || len(view.Models) != 1 || view.Models[0] != reachable {
		t.Fatalf("llm models --json returned %+v", view)
	}

	// "auto" is how the empty stored value is typed, and it is always allowed.
	if out, err := run(t, s, user, "llm default auto"); err != nil {
		t.Fatalf("llm default auto: %v (%s)", err, out)
	}
	if chosen, _ := s.db.UserDefaultModel(user.ID); chosen != "" {
		t.Fatalf("auto stored %q instead of clearing the choice", chosen)
	}
	// Leaving the argument off must stay a usage error: it cannot silently mean
	// auto, or a half-typed line would discard a choice.
	if _, err := run(t, s, user, "llm default"); err == nil {
		t.Error("\"llm default\" with no argument was accepted")
	}
}

// TestSSHLLMModelsWithoutEndpoint holds that an owner whose only connection is
// a provider credential is told why the list is empty rather than shown nothing.
func TestSSHLLMModelsWithoutEndpoint(t *testing.T) {
	s, user := accountServer(t)
	// A provider-native key reaches the agent as an environment credential, not
	// as a model row, so it contributes nothing to choose from.
	if _, err := run(t, s, user, "llm add openai "+theSecret); err != nil {
		t.Fatal(err)
	}
	out, err := run(t, s, user, "llm models")
	if err != nil {
		t.Fatalf("llm models: %v", err)
	}
	if !strings.Contains(out, "base URL") {
		t.Errorf("the empty model list does not explain itself:\n%s", out)
	}
}

// TestSSHKeyAdd covers the two ways a key arrives — quoted whole, or as the
// bare three fields the line splits into — and the two ways it is refused.
func TestSSHKeyAdd(t *testing.T) {
	s, user := accountServer(t)
	key := authorizedKey(t)

	if out, err := run(t, s, user, "ssh-key add laptop "+key+" owner@laptop"); err != nil {
		t.Fatalf("ssh-key add: %v (%s)", err, out)
	}
	stored, err := s.db.ListSSHKeysByUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0].Name != "laptop" {
		t.Fatalf("ssh-key add stored %+v", stored)
	}
	parsed, _, _, _, err := gossh.ParseAuthorizedKey([]byte(key))
	if err != nil {
		t.Fatal(err)
	}
	if want := gossh.FingerprintSHA256(parsed); stored[0].Fingerprint != want {
		t.Errorf("fingerprint %q, want %q", stored[0].Fingerprint, want)
	}

	// The same key under another name is still the same key, and the deployment
	// holds fingerprints unique.
	_, err = run(t, s, user, "ssh-key add desktop \""+key+"\"")
	if err == nil {
		t.Fatal("a duplicate key was accepted")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("duplicate refused with %q", err)
	}

	tests := []struct {
		name string
		line string
		want string
	}{
		{"not a key", "ssh-key add bad this-is-not-a-key", "not an OpenSSH public key"},
		{"key body missing", "ssh-key add bad", "give the whole public key"},
		// The dispatcher owns this now: it names the actions that do exist.
		{"unknown action", "ssh-key frobnicate", "has no action \"frobnicate\""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run(t, s, user, tc.line)
			if err == nil {
				t.Fatalf("%q was accepted", tc.line)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not explain %q", err, tc.want)
			}
		})
	}
	if again, _ := s.db.ListSSHKeysByUser(user.ID); len(again) != 1 {
		t.Errorf("a refused key was stored anyway: %+v", again)
	}
}

// TestSSHKeyListHidesTheKeyBody holds the one thing the listing must not do.
func TestSSHKeyListHidesTheKeyBody(t *testing.T) {
	s, user := accountServer(t)
	key := authorizedKey(t)
	if _, err := run(t, s, user, "ssh-key add laptop "+key); err != nil {
		t.Fatal(err)
	}
	// The base64 body, without the "ssh-ed25519 " prefix: that prefix is not a
	// secret and the fingerprint legitimately looks like base64 too.
	body := strings.Fields(key)[1]

	for _, line := range []string{"ssh-key list", "ssh-key list --json", "whoami", "whoami --json"} {
		out, err := run(t, s, user, line)
		if err != nil {
			t.Fatalf("%s: %v", line, err)
		}
		if strings.Contains(out, body) {
			t.Errorf("%q printed the key body:\n%s", line, out)
		}
		if !strings.Contains(out, "laptop") {
			t.Errorf("%q did not mention the key:\n%s", line, out)
		}
	}
}

// TestSSHKeyRemove holds that a key goes when it is named, and that the key
// holding the session open does not — deleting it from the machine that holds
// it is how an account locks itself out.
func TestSSHKeyRemove(t *testing.T) {
	s, user := accountServer(t)
	for _, name := range []string{"laptop", "desktop"} {
		if _, err := run(t, s, user, "ssh-key add "+name+" "+authorizedKey(t)); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := s.db.ListSSHKeysByUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := run(t, s, user, "ssh-key remove nosuchkey"); err == nil {
		t.Error("removing a key that does not exist was accepted")
	}

	// The session authenticated with the first key, so that one is refused while
	// the other is removed normally.
	authenticated, _, _, _, err := gossh.ParseAuthorizedKey([]byte(stored[0].PublicKey))
	if err != nil {
		t.Fatal(err)
	}
	sess := &keySession{key: authenticated}
	var buf bytes.Buffer
	err = s.exec(t.Context(), sess, newOut(&buf, false), user, "ssh-key remove "+stored[0].Name, false, false)
	if err == nil {
		t.Fatalf("the session's own key was removed: %s", buf.String())
	}
	if !strings.Contains(err.Error(), "this session authenticated with") {
		t.Errorf("refusal %q does not say why", err)
	}

	if out, err := run(t, s, user, "ssh-key remove "+stored[1].Name); err != nil {
		t.Fatalf("ssh-key remove: %v (%s)", err, out)
	}
	left, err := s.db.ListSSHKeysByUser(user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 || left[0].Name != stored[0].Name {
		t.Fatalf("ssh-key remove left %+v", left)
	}
}

// TestSSHWhoamiJSON holds that the account summary parses and carries what an
// agent needs to know about the account it is driving.
func TestSSHWhoamiJSON(t *testing.T) {
	s, user := accountServer(t)
	if err := s.db.CreateContainer(&db.Container{ID: "vm", Name: "dev", IncusName: "incus-dev", OwnerID: user.ID, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, s, user, "ssh-key add laptop "+authorizedKey(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, s, user, "llm add custom-lab "+theSecret+" --base-url=https://lab.example/v1 --models=gpt-5"); err != nil {
		t.Fatal(err)
	}
	chosen := db.UserModelID("custom-lab", "gpt-5")
	if _, err := run(t, s, user, "llm default "+chosen); err != nil {
		t.Fatal(err)
	}

	raw, err := run(t, s, user, "whoami --json")
	if err != nil {
		t.Fatalf("whoami --json: %v", err)
	}
	var view accountJSON
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatalf("whoami --json is not JSON: %v (%s)", err, raw)
	}
	switch {
	case view.Email != user.Email:
		t.Errorf("email %q, want %q", view.Email, user.Email)
	case view.Role != user.Role:
		t.Errorf("role %q, want %q", view.Role, user.Role)
	case view.Admin:
		t.Error("an ordinary account was reported as an administrator")
	case view.VMs != 1:
		t.Errorf("VM count %d, want 1", view.VMs)
	case view.DefaultModel != chosen:
		t.Errorf("default model %q, want %q", view.DefaultModel, chosen)
	case len(view.SSHKeys) != 1 || view.SSHKeys[0].Name != "laptop":
		t.Errorf("SSH keys %+v", view.SSHKeys)
	case view.SSHKeys[0].Fingerprint == "":
		t.Error("the key was listed without its fingerprint")
	}
	if strings.Contains(raw, theSecret) {
		t.Errorf("whoami returned an LLM credential:\n%s", raw)
	}
}
