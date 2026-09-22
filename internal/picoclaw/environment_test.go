package picoclaw

import (
	"os"
	"path/filepath"
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

// An agent that does not know the platform can be driven asks its owner to
// press buttons for it. Naming the management shell is only useful if it also
// says what the agent cannot do: it holds no key of the owner's.
func TestEnvironmentGuideNamesTheManagementShell(t *testing.T) {
	SSHPort = 2022
	t.Cleanup(func() { SSHPort = 2222 })

	guide := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000}, "example.com"))
	for _, want := range []string{
		"example.com port 2022",
		"help --json",
		"no private key of the owner's is installed here",
	} {
		if !strings.Contains(guide, want) {
			t.Errorf("guide never mentions %q:\n%s", want, guide)
		}
	}

	// Without a domain there is no address to send the agent to.
	if bare := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000}, "")); strings.Contains(bare, "help --json") {
		t.Errorf("guide promises a management shell the deployment has no address for:\n%s", bare)
	}
}

// The owner's own domain is the address they will share, so the agent has to
// name it when it reports where the work is.
func TestEnvironmentGuideNamesVerifiedAliases(t *testing.T) {
	c := &db.Container{Name: "demo", AppPort: 8080, AppPublic: true, Aliases: []*db.ContainerAlias{
		{Hostname: "app.example.org", Verified: true},
		// An unverified alias is not routed, so promising it would have the
		// agent report an address that answers 404.
		{Hostname: "pending.example.org"},
	}}
	guide := string(environmentGuide(c, "example.com"))

	if !strings.Contains(guide, "https://app.example.org/") {
		t.Errorf("guide never mentions the verified alias:\n%s", guide)
	}
	if strings.Contains(guide, "pending.example.org") {
		t.Errorf("guide promises an unverified alias:\n%s", guide)
	}
}

// A custom domain cannot carry the owner's session, so it serves nothing until
// the workload is published — the agent must not claim otherwise.
func TestEnvironmentGuideWarnsAliasesNeedAPublishedPort(t *testing.T) {
	c := &db.Container{Name: "demo", AppPort: 8080, Aliases: []*db.ContainerAlias{
		{Hostname: "app.example.org", Verified: true},
	}}
	guide := string(environmentGuide(c, "example.com"))
	if !strings.Contains(guide, "until the owner publishes it") {
		t.Errorf("guide does not warn that a private port leaves the alias dead:\n%s", guide)
	}

	c.AppPublic = true
	published := string(environmentGuide(c, "example.com"))
	if strings.Contains(published, "until the owner publishes it") {
		t.Errorf("guide warns about publishing a port that is already published:\n%s", published)
	}
}

// A VM with no custom domains must not gain a paragraph about them.
func TestEnvironmentGuideSilentWithoutAliases(t *testing.T) {
	guide := string(environmentGuide(&db.Container{Name: "demo", AppPort: 3000}, "example.com"))
	if strings.Contains(guide, "their own domains") {
		t.Errorf("guide talks about custom domains the VM does not have:\n%s", guide)
	}
}

// Setup must plant the guide inside the VM, readable by the agent's user and
// writable only by the gateway.
func TestSetupWritesEnvironmentGuide(t *testing.T) {
	original := Domain
	Domain = "example.com"
	t.Cleanup(func() { Domain = original })

	guest := &guestRuntime{files: map[string][]byte{}}
	if err := writeEnvironmentGuide(t.Context(), guest, nil, testContainer("owner")); err != nil {
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

// Setup runs on every create, start and recreate. If it did not load the VM's
// custom domains itself, a restart would quietly drop them from the guide and
// the agent would go back to reporting only the platform host.
func TestSetupLoadsAliasesIntoTheGuide(t *testing.T) {
	original := Domain
	Domain = "example.com"
	t.Cleanup(func() { Domain = original })

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
	c := testContainer(owner.ID)
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	alias, err := database.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetAliasVerification(alias.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	guest := &guestRuntime{files: map[string][]byte{}}
	// The container is passed in exactly as a start path would build it, with
	// no aliases attached.
	if err := SetupContainer(t.Context(), guest, database, nil, testContainer(owner.ID), nil); err != nil {
		t.Fatal(err)
	}
	if guide := string(guest.files[GuideFilePath]); !strings.Contains(guide, "https://app.example.org/") {
		t.Errorf("setup wrote a guide without the VM's custom domain:\n%s", guide)
	}
}

// Refresh runs when the owner flips the publish switch, and it is handed the
// container that handler happens to be holding — which carries no aliases. If
// the guide did not load them itself, toggling publish would erase the owner's
// domains from the guide exactly when the paragraph about them changes.
func TestRefreshEnvironmentGuideKeepsAliases(t *testing.T) {
	original := Domain
	Domain = "example.com"
	t.Cleanup(func() { Domain = original })

	database, err := db.Open(filepath.Join(t.TempDir(), "gateway.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	owner, err := database.EnsureUser("owner", "owner@example.com")
	if err != nil {
		t.Fatal(err)
	}
	c := testContainer(owner.ID)
	if err := database.CreateContainer(c); err != nil {
		t.Fatal(err)
	}
	alias, err := database.CreateContainerAlias(c.ID, "app.example.org")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.SetAliasVerification(alias.ID, true, ""); err != nil {
		t.Fatal(err)
	}

	guest := &guestRuntime{files: map[string][]byte{}}
	// Exactly what the publish handlers pass: a freshly read container, with
	// nothing attached to it.
	fresh, err := database.GetContainerByID(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Aliases != nil {
		t.Fatal("the fixture is wrong: a plain read must not carry aliases")
	}
	if err := RefreshEnvironmentGuide(t.Context(), guest, database, fresh); err != nil {
		t.Fatal(err)
	}
	if guide := string(guest.files[GuideFilePath]); !strings.Contains(guide, "https://app.example.org/") {
		t.Errorf("refresh erased the owner's custom domain from the guide:\n%s", guide)
	}
}

// A stopped VM cannot be written to; the change reaches it on its next start.
func TestRefreshEnvironmentGuideSkipsStoppedVMs(t *testing.T) {
	guest := &guestRuntime{files: map[string][]byte{}}
	c := testContainer("owner")
	c.Status = "stopped"
	if err := RefreshEnvironmentGuide(t.Context(), guest, nil, c); err != nil {
		t.Fatal(err)
	}
	if len(guest.commands) != 0 {
		t.Errorf("talked to a stopped VM: %v", guest.commands)
	}
}
