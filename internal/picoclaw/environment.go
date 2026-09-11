package picoclaw

import (
	"context"
	"fmt"
	"log"
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

	// The owner's own domains are the addresses they will actually share, so
	// the agent has to name those rather than the platform host when it reports
	// that something is live. Only verified aliases are routed at all.
	if verified := verifiedAliases(c); len(verified) > 0 {
		fmt.Fprintf(&b, "- The owner has pointed their own domains at this VM: %s. They reach the same port **%d** as the address above — prefer them when you tell the owner where their work is.\n",
			strings.Join(verified, ", "), c.AppPort)
		if !c.AppPublic {
			b.WriteString("  Those domains only serve traffic while the port above is published, so they answer with an error until the owner publishes it.\n")
		}
	}

	// Left unsaid, an agent discovers this by trying: it installs Docker, watches
	// the daemon fail to create a container, concludes the platform forbids it
	// and rebuilds everything from source. Stating the answer up front is the
	// difference between minutes and a wasted afternoon — in both directions.
	// What matters to the agent is what the running container actually booted
	// with, not what the owner has since asked for: a setting that is still
	// waiting on a restart would have the agent try Docker and fail.
	b.WriteString("\n## Containers inside this VM\n\n")
	if c.NestingApplied {
		b.WriteString("- Nested containers are **enabled** here. Docker, buildah and nested Incus can create containers, so prefer an image-based workflow over building from source when the project ships one.\n")
		b.WriteString("- Docker is not installed by default. `sudo apt-get install -y docker.io` works, and the daemon comes up normally.\n")
		fmt.Fprintf(&b, "- A container of yours is only reachable from outside if its published port is the VM's own port **%d** — publish it with `-p %d:<container-port>`.\n", c.AppPort, c.AppPort)
	} else {
		b.WriteString("- Nested containers are **disabled** here, so Docker cannot create a single one however it is installed. This is a platform setting, not a broken install: do not spend time diagnosing the daemon.\n")
		b.WriteString("- Build and run from source instead, and say so when you report what you did. The owner can turn nesting on from their dashboard and restart this VM if a container is genuinely needed.\n")
	}

	b.WriteString("\n## Making a service reachable\n\n")
	b.WriteString("- Bind to `0.0.0.0`, not `127.0.0.1`. The platform proxies to this container's own address, so a service listening only on loopback is unreachable from the outside even though `curl localhost` works here.\n")
	fmt.Fprintf(&b, "- Configure the port explicitly. If the software cannot use %d, tell the owner which port it needs so they can repoint the VM, rather than leaving it on a port nobody can reach.\n", c.AppPort)
	b.WriteString("- Run it under systemd (`sudo systemctl enable --now <unit>`) so it survives a restart of this VM. A process started from a shell dies with your session.\n")
	b.WriteString("- Verify from inside the VM with `curl -sS localhost:<port>` before reporting that it works, and mention the https address above when you report it.\n")
	return []byte(b.String())
}

// verifiedAliases renders the VM's custom domains as https URLs, skipping the
// ones whose DNS check has not passed: an unverified alias is not routed, so
// promising it to the agent would have it report an address that answers 404.
func verifiedAliases(c *db.Container) []string {
	var hosts []string
	for _, a := range c.Aliases {
		if a != nil && a.Verified {
			hosts = append(hosts, "**https://"+a.Hostname+"/**")
		}
	}
	return hosts
}

// writeEnvironmentGuide publishes the guide inside the VM. The agent reads it
// on every new conversation, so refreshing the file is enough to change what
// the agent believes about this VM.
// It loads the VM's custom domains itself rather than trusting the caller to
// have attached them. Every path that rewrites the guide — first setup, a
// publish-setting change, and an alias being added or removed — would otherwise
// have to remember, and a caller that forgot would silently erase the owner's
// domains from the guide instead of failing.
//
// database may be nil, which simply produces a guide without custom domains.
func writeEnvironmentGuide(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if c == nil {
		return nil
	}
	// A domain list that cannot be read is left out rather than fatal: the VM
	// working matters more than the guide being complete.
	if database != nil {
		if err := database.AttachAliases(c); err != nil {
			log.Printf("agent guide for %s: load custom domains: %v", c.IncusName, err)
		}
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
func RefreshEnvironmentGuide(ctx context.Context, rt runtime.ContainerRuntime, database *db.DB, c *db.Container) error {
	if rt == nil || c == nil || !strings.EqualFold(c.Status, "running") {
		return nil
	}
	return writeEnvironmentGuide(ctx, rt, database, c)
}
