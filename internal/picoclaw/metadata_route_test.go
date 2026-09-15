package picoclaw

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/metadata"
)

func agentBinary(t *testing.T) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "picoclaw")
	if err := os.WriteFile(binary, []byte("agent-binary"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SVKEXE_AGENT_BINARY", binary)
}

// metadataServed puts the package in the state a deployment that actually serves
// the endpoint is in, and restores it afterwards so the default — not served —
// keeps governing every other test.
func metadataServed(t *testing.T) {
	t.Helper()
	previous := MetadataAvailable
	MetadataAvailable = true
	t.Cleanup(func() { MetadataAvailable = previous })
}

// Setup is the one step every create, start, recreate, SSH-menu and boot
// reconcile path goes through, which is what lets a VM that predates the
// metadata service pick the route up without anybody migrating it.
func TestSetupInstallsTheMetadataRoute(t *testing.T) {
	agentBinary(t)
	metadataServed(t)
	guest := &guestRuntime{files: map[string][]byte{}}

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err != nil {
		t.Fatal(err)
	}

	if script := string(guest.files[metadata.GuestRouteScriptPath]); script == "" {
		t.Fatal("the route helper was not written into the VM")
	}
	if unit := string(guest.files[metadata.GuestRouteUnitPath]); !strings.Contains(unit, "ExecStart="+metadata.GuestRouteScriptPath) {
		t.Errorf("the unit does not run the helper: %q", unit)
	}

	commands := strings.Join(guest.commands, "\n")
	for _, want := range []string{
		"chmod 755 " + metadata.GuestRouteScriptPath,
		"systemctl enable " + metadata.GuestRouteUnitName,
	} {
		if !strings.Contains(commands, want) {
			t.Errorf("setup never ran %q", want)
		}
	}
}

// A deployment that cannot serve the endpoint — the gateway in its own network
// namespace, or METADATA_ADDR=off — must not configure VMs for it.
func TestNoRouteIsInstalledWhereTheServiceIsNotServed(t *testing.T) {
	agentBinary(t)
	guest := &guestRuntime{files: map[string][]byte{}}

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err != nil {
		t.Fatal(err)
	}
	if _, installed := guest.files[metadata.GuestRouteUnitPath]; installed {
		t.Error("a deployment that does not serve metadata still configured the VM for it")
	}
	if strings.Contains(strings.Join(guest.commands, "\n"), metadata.GuestRouteUnitName) {
		t.Error("the guest was asked about a unit that should never have been installed")
	}
}

// Setup runs on thirteen call sites including every SSH-menu VM selection, so a
// VM that already has this version must not pay for two file pushes and a
// daemon-reload each time.
func TestASecondSetupDoesNotReinstallTheRoute(t *testing.T) {
	agentBinary(t)
	metadataServed(t)
	guest := &guestRuntime{files: map[string][]byte{}}

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err != nil {
		t.Fatal(err)
	}
	stamp := contentStamp(metadata.GuestRouteScript(), metadata.GuestRouteUnit())
	if !strings.Contains(strings.Join(guest.commands, "\n"), stamp) {
		t.Fatal("no stamp was written, so a repeat setup has nothing to compare against")
	}

	// The guest now reports the stamp and an enabled unit, which is what a real
	// VM would do on the next setup.
	guest.commands = nil
	guest.metadataRouteStamp = stamp

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err != nil {
		t.Fatal(err)
	}
	commands := strings.Join(guest.commands, "\n")
	if strings.Contains(commands, "systemctl enable "+metadata.GuestRouteUnitName) {
		t.Error("an unchanged route was installed again")
	}
	if strings.Contains(commands, "chmod 755 "+metadata.GuestRouteScriptPath) {
		t.Error("an unchanged route was rewritten")
	}
}

// A VM reaches the metadata service through its default route whether or not
// this pin was installed, so failing to install it must not cost the owner a VM.
func TestAFailedMetadataRouteDoesNotFailSetup(t *testing.T) {
	agentBinary(t)
	metadataServed(t)
	guest := &guestRuntime{files: map[string][]byte{}, fail: "systemctl enable " + metadata.GuestRouteUnitName}

	if err := SetupContainer(t.Context(), guest, nil, nil, testContainer("owner"), nil); err != nil {
		t.Fatalf("setup failed over the metadata route: %v", err)
	}
	// The rest of setup still happened.
	if _, ok := guest.files[ConfigFilePath]; !ok {
		t.Error("setup did not get as far as writing the agent configuration")
	}
}

// The helper has to survive being run twice and being run early, because the
// gateway reruns it on every setup and systemd runs it before DHCP may have
// finished.
func TestGuestRouteScriptIsIdempotentAndSelfContained(t *testing.T) {
	script := metadata.GuestRouteScript()

	if !strings.Contains(script, "ip route replace") {
		t.Error("the helper must use 'ip route replace' so a second run is a no-op")
	}
	if strings.Contains(script, "ip route add") {
		t.Error("'ip route add' fails on an existing route; the helper must not use it")
	}
	if !strings.Contains(script, metadata.Address+"/32") {
		t.Errorf("the helper must pin a /32 to beat a zeroconf 169.254.0.0/16 route:\n%s", script)
	}
	if !strings.Contains(script, "ip -4 route show default") {
		t.Error("the helper must derive the route from the default route")
	}
	// The host holds no such address and answers no ARP for it — it redirects
	// the traffic on the bridge instead — so an on-link route reaches nothing.
	if strings.Contains(script, "dev eth0") {
		t.Error("the helper must not install an on-link route; the host answers no ARP for the address")
	}
	if !strings.Contains(script, `via "${gw}"`) {
		t.Error("the route must go via the default gateway")
	}
	if !strings.Contains(script, "exit 1") {
		t.Error("a boot with no default route yet must fail so the unit's restart retries it")
	}
	if !strings.HasPrefix(script, "#!/bin/sh\n") {
		t.Error("the helper must carry a shebang; systemd ExecStart does not use a shell")
	}
	if !strings.Contains(script, "set -eu") {
		t.Error("the helper must fail loudly so the unit's restart can retry it")
	}
	// Five retries five seconds apart otherwise put the unit in a permanent
	// failed state on the slow-booting VM this exists for.
	if !strings.Contains(metadata.GuestRouteUnit(), "StartLimitIntervalSec=0") {
		t.Error("the unit must not be subject to systemd's start rate limit")
	}
}

// An agent that does not know the endpoint exists asks the owner for facts the
// VM already answers, or guesses them.
func TestTheGuideNamesTheMetadataEndpoint(t *testing.T) {
	metadataServed(t)
	guide := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000, Nesting: true, NestingApplied: true,
	}, "example.com"))

	if !strings.Contains(guide, "http://"+metadata.Address+"/latest/meta-data/") {
		t.Errorf("the guide never names the metadata endpoint:\n%s", guide)
	}
	for _, key := range []string{"instance-id", "svkexe/app-port", "public-keys/0/openssh-key"} {
		if !strings.Contains(guide, key) {
			t.Errorf("the guide does not point at %s", key)
		}
	}
	// Without this an agent pipes a value into something that splits on lines
	// and wonders why the last field is empty.
	if !strings.Contains(guide, "no trailing newline") {
		t.Error("the guide does not describe the response format")
	}
}

// Promising an endpoint that does not answer costs the agent turns to discover,
// and there is no way for it to tell a dead address from a slow one.
func TestTheGuideStaysSilentWhereTheServiceIsNotServed(t *testing.T) {
	guide := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000, Nesting: true, NestingApplied: true,
	}, "example.com"))

	if strings.Contains(guide, metadata.Address) {
		t.Errorf("the guide promises an endpoint this deployment does not serve:\n%s", guide)
	}
}
