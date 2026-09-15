package picoclaw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/skrashevich/svkexe/internal/metadata"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// MetadataAvailable says whether this deployment serves the instance metadata
// service. It mirrors the gateway's METADATA_ADDR the way Domain mirrors DOMAIN,
// and it gates two things: installing the route inside a VM, and promising the
// endpoint in the agent's guide. A deployment that cannot hold the address — the
// Docker Compose variant, or one with METADATA_ADDR=off — would otherwise send
// every agent to curl a dead address.
var MetadataAvailable bool

// metadataRouteStamp records which version of the route helper a VM already has.
// It is what keeps the common case to a single command: the content is fixed at
// build time, so a VM set up ten times over is configured once. It lives in the
// gateway's own configuration directory rather than in systemd's unit directory,
// which is for units.
var metadataRouteStamp = ConfigDir + "/metadata-route.stamp"

// installMetadataRoute makes http://169.254.169.254/ answer inside the VM.
//
// It runs on every setup rather than only on creation, which is what reaches the
// VMs that existed before the metadata service did: setup is the one step every
// create, start, recreate, SSH-menu and boot-reconcile path goes through. There
// are thirteen such call sites, including every SSH-menu VM selection, so the
// repeat is made cheap rather than merely idempotent — a VM already carrying this
// version costs one command and no daemon-reload.
func installMetadataRoute(ctx context.Context, rt runtime.ContainerRuntime, name string) error {
	if !MetadataAvailable {
		return nil
	}
	script, unit := metadata.GuestRouteScript(), metadata.GuestRouteUnit()
	stamp := contentStamp(script, unit)

	// An enabled unit with a matching stamp is already what this would install.
	// is-enabled is checked too, so a VM whose unit was disabled by hand is
	// repaired rather than trusted.
	probe := fmt.Sprintf("test \"$(cat %s 2>/dev/null)\" = %s && systemctl is-enabled --quiet %s",
		metadataRouteStamp, stamp, metadata.GuestRouteUnitName)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-c", probe}); err == nil {
		return nil
	}

	if err := writeGuestFile(ctx, rt, name, metadata.GuestRouteScriptPath, []byte(script)); err != nil {
		return err
	}
	if err := writeGuestFile(ctx, rt, name, metadata.GuestRouteUnitPath, []byte(unit)); err != nil {
		return err
	}
	// -e, so a failed chmod or daemon-reload is a failed install rather than
	// something the trailing stamp write papers over. writeGuestFile writes under
	// umask 077, so the script arrives unexecutable and the chmod is load-bearing.
	//
	// `enable` and `start` are separated deliberately. Enabling must succeed —
	// it is what makes the route survive a reboot. Starting is allowed to fail,
	// and routinely does during VM creation: the helper exits non-zero until DHCP
	// has produced a default route, and the unit's own Restart= is what retries
	// it. Folding the two together would make that ordinary case look like a
	// broken install, and a genuine failure look like that ordinary case.
	//
	// The stamp is written last, so an install interrupted halfway is retried
	// rather than skipped.
	install := fmt.Sprintf(
		"chmod 755 %s\nsystemctl daemon-reload\nsystemctl enable %s\nsystemctl start %s || true\nprintf '%%s' %s > %s\n",
		metadata.GuestRouteScriptPath,
		metadata.GuestRouteUnitName, metadata.GuestRouteUnitName,
		stamp, metadataRouteStamp)
	if _, err := rt.Exec(ctx, name, []string{"sh", "-euc", install}); err != nil {
		return fmt.Errorf("enable %s: %w", metadata.GuestRouteUnitName, err)
	}
	return nil
}

// contentStamp is a short digest of what would be installed, so a change to
// either file is enough to make a VM take the new version.
func contentStamp(script, unit string) string {
	sum := sha256.Sum256([]byte(script + "\x00" + unit))
	return hex.EncodeToString(sum[:8])
}
