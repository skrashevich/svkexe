package sshgw

import (
	"bytes"
	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/integrations"
	"strings"
	"testing"
)

type labIntegration struct{}

func (labIntegration) Descriptor() integrations.Descriptor {
	return integrations.Descriptor{ID: "lab", Name: "Lab", Config: []integrations.Field{{Name: "account", Required: true}}, Secrets: []integrations.Field{{Name: "key", Required: true}, {Name: "password", Required: true}}, Credentials: []string{"key", "password"}}
}
func (labIntegration) Validate(integrations.Input) error { return nil }
func (labIntegration) Credential(in integrations.Input, n string) (string, error) {
	return in.Secrets[n], nil
}
func TestSSHIntegrationInputAndIsolation(t *testing.T) {
	s, user := accountServer(t)
	s.integrationService = integrations.New(s.db, s.encKey, integrations.GitHub{}, labIntegration{})
	session := newSession(t, user, "svkexe", "integration add lab", `{"config":{"account":"team"},"secrets":{"key":"private-key","password":"private-password"}}`)
	var out bytes.Buffer
	if err := s.exec(t.Context(), session, newOut(&out, false), user, "integration add lab", false, false); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "private-") {
		t.Fatal("save leaked secret")
	}
	for _, command := range []string{"integration list", "integration list --json", "integration providers --json"} {
		text, err := run(t, s, user, command)
		if err != nil || !strings.Contains(text, "lab") || strings.Contains(text, "private-") {
			t.Fatal(command, text, err)
		}
	}
	other := &db.User{ID: "other", Email: "other@test", Role: "user"}
	if err := s.db.CreateUser(other); err != nil {
		t.Fatal(err)
	}
	if _, err := run(t, s, other, "integration rm lab"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.integrationService.Credential(t.Context(), user.ID, "lab", "password"); err != nil || got != "private-password" {
		t.Fatal("foreign remove")
	}
	bad := newSession(t, user, "svkexe", "integration add github", `{"secrets":{"token":"private-token"},"unexpected":"private-token"}`)
	if err := s.exec(t.Context(), bad, newOut(&out, false), user, "integration add github", false, false); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("bad input accepted or leaked")
	}
	if _, err := run(t, s, user, "integration rm lab"); err != nil {
		t.Fatal(err)
	}
	guest := &db.User{ID: "guest", Role: db.GuestRole}
	if _, err := run(t, s, guest, "integration list"); err == nil {
		t.Fatal("guest command allowed")
	}
}
