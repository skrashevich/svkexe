package picoclaw

import (
	"context"
	"fmt"
	"strings"

	"github.com/skrashevich/svkexe/internal/db"
	"github.com/skrashevich/svkexe/internal/runtime"
)

// GuideFilePath is where the gateway describes the VM to the agent. The agent
// reads it into its system prompt, so it knows the address its work will be
// served on before it configures anything. It lives beside the rest of the
// gateway-owned configuration: the user owns their home directory and could
// otherwise overwrite what only the gateway knows.
const GuideFilePath = ConfigDir + "/AGENTS.md"

// Domain is the base domain VM hosts are built from, set once at startup. It
// mirrors the gateway's DOMAIN setting; empty means subdomain routing is not
// configured and the agent is told only about the local ports.
var Domain string

// environmentGuide describes, in the agent's own terms, where the VM's work
// becomes reachable. Without it an agent configures services blind: it binds
// to localhost, picks whatever port the upstream README suggests, and the
// result is unreachable from the address the owner was given.
func environmentGuide(c *db.Container, domain string) []byte {
	var b strings.Builder
	b.WriteString("# This VM\n\n")
	b.WriteString("You are running inside a container that the platform publishes for its owner. ")
	b.WriteString("What you install here is reached through the addresses below, not through localhost.\n\n")

	if domain == "" {
		// Without a domain there is no external address to promise, but the
		// port contract still holds and still decides how services must bind.
		fmt.Fprintf(&b, "- The published port of this VM is **%d**. A service is only reachable if it listens on that port.\n", c.AppPort)
	} else {
		host := c.Name + "." + domain
		fmt.Fprintf(&b, "- **Your work is served at https://%s/** and the platform forwards it to port **%d** inside this VM. ", host, c.AppPort)
		b.WriteString("Serve the thing you are asked to build on that port unless the owner says otherwise.\n")
		fmt.Fprintf(&b, "- Any other port is reachable at **https://<port>-%s.%s/** — for example https://8080-%s.%s/ for port 8080.\n", c.Name, domain, c.Name, domain)
		fmt.Fprintf(&b, "- The agent interface you are speaking through is at https://%s%s.%s/. Port %d is reserved for it: never bind a service to that port.\n",
			db.AgentHostPrefix, c.Name, domain, Port)
		if c.AppPublic {
			fmt.Fprintf(&b, "- https://%s/ is **public**: anyone can reach it without signing in. Everything else requires the owner's session. Do not serve secrets from port %d.\n", host, c.AppPort)
		} else {
			fmt.Fprintf(&b, "- https://%s/ is **private**: only the signed-in owner reaches it. The owner can publish it from the dashboard.\n", host)
		}
	}

	b.WriteString("\n## Making a service reachable\n\n")
	b.WriteString("- Bind to `0.0.0.0`, not `127.0.0.1`. The platform proxies to this container's own address, so a service listening only on loopback is unreachable from the outside even though `curl localhost` works here.\n")
	fmt.Fprintf(&b, "- Configure the port explicitly. If the software cannot use %d, tell the owner which port it needs so they can repoint the VM, rather than leaving it on a port nobody can reach.\n", c.AppPort)
	b.WriteString("- Run it under systemd (`sudo systemctl enable --now <unit>`) so it survives a restart of this VM. A process started from a shell dies with your session.\n")
	b.WriteString("- Verify from inside the VM with `curl -sS localhost:<port>` before reporting that it works, and mention the https address above when you report it.\n")
	return []byte(b.String())
}

// writeEnvironmentGuide publishes the guide inside the VM. The agent reads it
// on every new conversation, so refreshing the file is enough to change what
// the agent believes about this VM.
func writeEnvironmentGuide(ctx context.Context, rt runtime.ContainerRuntime, c *db.Container) error {
	if c == nil {
		return nil
	}
	if err := writeGuestFile(ctx, rt, c.IncusName, GuideFilePath, environmentGuide(c, Domain)); err != nil {
		return err
	}
	// Readable by the agent's unprivileged user, writable only by the gateway.
	protect := fmt.Sprintf("chown root:%s %[2]s && chmod 640 %[2]s", ContainerUser, GuideFilePath)
	_, err := rt.Exec(ctx, c.IncusName, []string{"sh", "-c", protect})
	return err
}

// RefreshEnvironmentGuide updates a running VM after its publishing settings
// change, so the next conversation sees the port and visibility the owner just
// chose. A stopped VM picks the change up when it next starts.
func RefreshEnvironmentGuide(ctx context.Context, rt runtime.ContainerRuntime, c *db.Container) error {
	if rt == nil || c == nil || !strings.EqualFold(c.Status, "running") {
		return nil
	}
	return writeEnvironmentGuide(ctx, rt, c)
}
