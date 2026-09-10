package picoclaw

import (
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

// testContainer is the VM every setup test operates on.
func testContainer(owner string) *db.Container {
	return &db.Container{ID: "id", Name: "vm", OwnerID: owner, IncusName: "vm", Status: "running", AppPort: db.DefaultAppPort}
}

// The agent configures software for whatever address it believes in. Getting
// the host or the port wrong here produces a service nobody can reach, so the
// exact values have to be in the guide.
func TestEnvironmentGuideNamesHostAndPort(t *testing.T) {
	c := &db.Container{Name: "demo", IncusName: "svkexe-o-demo", AppPort: 8080}
	guide := string(environmentGuide(c, "example.com"))

	for _, want := range []string{
		"https://demo.example.com/",
		"port **8080**",
		"https://<port>-demo.example.com/",
		"https://agent-demo.example.com/",
		"0.0.0.0",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide never mentions %q:\n%s", want, guide)
		}
	}
	// The agent port belongs to the agent; a service placed there would answer
	// on the host that grants command execution.
	if !strings.Contains(guide, "never bind a service to that port") {
		t.Error("guide does not reserve the agent port")
	}
}

func TestEnvironmentGuideStatesVisibility(t *testing.T) {
	private := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000}, "example.com"))
	if !strings.Contains(private, "**private**") {
		t.Errorf("a private VM is not described as private:\n%s", private)
	}
	public := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000, AppPublic: true}, "example.com"))
	if !strings.Contains(public, "**public**") || !strings.Contains(public, "without signing in") {
		t.Errorf("a published VM is not described as public:\n%s", public)
	}
}

// A gateway without DOMAIN has no external address to promise, but the port
// contract still decides whether a service is reachable at all.
func TestEnvironmentGuideWithoutDomain(t *testing.T) {
	guide := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000}, ""))
	if strings.Contains(guide, "https://") {
		t.Errorf("guide promises an address that does not exist:\n%s", guide)
	}
	if !strings.Contains(guide, "**3000**") || !strings.Contains(guide, "0.0.0.0") {
		t.Errorf("guide drops the port contract:\n%s", guide)
	}
}

// Setup must plant the guide inside the VM, readable by the agent's user and
// writable only by the gateway.
func TestSetupWritesEnvironmentGuide(t *testing.T) {
	original := Domain
	Domain = "example.com"
	t.Cleanup(func() { Domain = original })

	guest := &guestRuntime{files: map[string][]byte{}}
	if err := writeEnvironmentGuide(t.Context(), guest, testContainer("owner")); err != nil {
		t.Fatal(err)
	}
	guide := string(guest.files[GuideFilePath])
	if !strings.Contains(guide, "https://vm.example.com/") {
		t.Errorf("guide not written into the VM: %q", guide)
	}
	if script := strings.Join(guest.commands, "\n"); !strings.Contains(script, "chmod 640 "+GuideFilePath) {
		t.Errorf("guide left writable by the VM's user: %s", script)
	}
}

// A stopped VM cannot be written to; the change reaches it on its next start.
func TestRefreshEnvironmentGuideSkipsStoppedVMs(t *testing.T) {
	guest := &guestRuntime{files: map[string][]byte{}}
	c := testContainer("owner")
	c.Status = "stopped"
	if err := RefreshEnvironmentGuide(t.Context(), guest, c); err != nil {
		t.Fatal(err)
	}
	if len(guest.commands) != 0 {
		t.Errorf("talked to a stopped VM: %v", guest.commands)
	}
}
