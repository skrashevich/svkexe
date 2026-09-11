package picoclaw

import (
	"strings"
	"testing"

	"github.com/skrashevich/svkexe/internal/db"
)

// Left to find out for itself, an agent installs Docker, watches the daemon
// fail to create a container, concludes the platform forbids it and rebuilds
// the whole project from source. The guide has to answer the question before
// the agent spends an afternoon on it.
func TestTheGuideTellsTheAgentWhetherDockerCanWork(t *testing.T) {
	enabled := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000, Nesting: true, NestingApplied: true,
	}, "example.com"))
	if !strings.Contains(enabled, "**enabled**") {
		t.Errorf("a VM that can run containers is not told so:\n%s", enabled)
	}
	if !strings.Contains(enabled, "docker.io") {
		t.Error("the guide does not say how to get Docker")
	}

	disabled := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000,
	}, "example.com"))
	if !strings.Contains(disabled, "**disabled**") {
		t.Errorf("a VM that cannot run containers is not told so:\n%s", disabled)
	}
	// The failure looks exactly like a broken daemon, so the guide has to rule
	// that out explicitly or the agent debugs the wrong thing.
	if !strings.Contains(disabled, "not a broken install") {
		t.Error("the guide leaves the agent to diagnose the daemon")
	}
	if !strings.Contains(disabled, "from source") {
		t.Error("the guide does not point at the workflow that does work")
	}
}

// What matters to the agent is what the running container booted with. A
// setting the owner has ticked but not yet restarted into would send the agent
// down the Docker path against a container that still cannot use it.
func TestTheGuideReportsWhatTheVMBootedWithNotWhatWasAskedFor(t *testing.T) {
	pending := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000, Nesting: true, NestingApplied: false,
	}, "example.com"))
	if !strings.Contains(pending, "**disabled**") {
		t.Errorf("a VM still owing a restart claims Docker works:\n%s", pending)
	}

	// And the reverse: nesting stays live until the VM restarts, even after the
	// owner turns it off.
	stillOn := string(environmentGuide(&db.Container{
		Name: "demo", AppPort: 3000, Nesting: false, NestingApplied: true,
	}, "example.com"))
	if !strings.Contains(stillOn, "**enabled**") {
		t.Errorf("a VM that is still running with nesting is told it has none:\n%s", stillOn)
	}
}
