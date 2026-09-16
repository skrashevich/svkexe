package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The platform describes the VM — the host it answers on, the port published
// to the outside — in a file the agent must read before configuring anything,
// or it will bind a service to a port nobody can reach.
func TestPlatformAgentsFileReachesTheSystemPrompt(t *testing.T) {
	guide := filepath.Join(t.TempDir(), "AGENTS.md")
	const text = "This VM is served at https://demo.example.com/ from port 3000."
	if err := os.WriteFile(guide, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	original := platformAgentsFile
	platformAgentsFile = guide
	t.Cleanup(func() { platformAgentsFile = original })

	prompt, err := GenerateSystemPrompt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, text) {
		t.Errorf("the VM's own hosting details never reached the prompt")
	}
}

// A VM without the file must still produce a prompt: the agent has to work on
// hosts that predate the platform guide.
func TestMissingPlatformAgentsFileIsNotAnError(t *testing.T) {
	original := platformAgentsFile
	platformAgentsFile = filepath.Join(t.TempDir(), "absent.md")
	t.Cleanup(func() { platformAgentsFile = original })

	if _, err := GenerateSystemPrompt(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
