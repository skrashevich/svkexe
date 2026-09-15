package metadata

import (
	"encoding/json"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skrashevich/svkexe/internal/db"
)

// node is one entry of the metadata tree. A node with children is a directory
// and answers with a listing of them; a node without is a leaf and answers with
// its value.
//
// Listings and values come from the same structure on purpose. Built as two
// separate things they drift: a key gets added to the listing and never
// implemented, or implemented and never listed, and a caller that walks the tree
// — which is what every discovery tool does — hits a 404 on a path the service
// itself advertised.
type node struct {
	name string
	// label is how the node appears in its parent's listing when that differs
	// from the path segment. Only the public-keys slots need it: EC2 lists them
	// as "0=my-key" while the canonical path is ".../public-keys/0/openssh-key".
	label    string
	value    string
	children []*node
}

func (n *node) isDir() bool { return len(n.children) > 0 }

// child returns the named child, or nil. A labelled child answers to either
// spelling, because a caller that pasted a listing line straight into a path is
// asking for the node it just read about.
func (n *node) child(name string) *node {
	for _, c := range n.children {
		if c.name == name || (c.label != "" && c.label == name) {
			return c
		}
	}
	return nil
}

// listing renders a directory the way EC2 does: one entry per line, directories
// suffixed with a slash, and the last line terminated like every other. A
// labelled entry is printed as its label, verbatim and unsuffixed, which is what
// EC2 does with the "<index>=<name>" key slots.
func (n *node) listing() string {
	var b strings.Builder
	for _, c := range n.children {
		if c.label != "" {
			b.WriteString(c.label)
			b.WriteString("\n")
			continue
		}
		b.WriteString(c.name)
		if c.isDir() {
			b.WriteString("/")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func leaf(name, value string) *node { return &node{name: name, value: value} }

// dir builds a directory, dropping nil children so that a caller can make an
// entry conditional inline, and sorting them so the listing order cannot drift
// with the order the code happens to build them in.
func dir(name string, children ...*node) *node {
	kept := make([]*node, 0, len(children))
	for _, c := range children {
		if c != nil {
			kept = append(kept, c)
		}
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].name < kept[j].name })
	return &node{name: name, children: kept}
}

// optional returns a leaf only when there is something to publish. An empty key
// is left out entirely rather than served as an empty string: a listing is the
// service's own promise that every entry answers, and "" answers nothing.
func optional(name, value string) *node {
	if value == "" {
		return nil
	}
	return leaf(name, value)
}

// buildTree assembles the /latest subtree for one caller.
func buildTree(id *Identity, cfg Config) *node {
	return dir("latest",
		dir("dynamic",
			dir("instance-identity",
				leaf("document", identityDocument(id, cfg)),
			),
		),
		metaData(id, cfg),
	)
}

func metaData(id *Identity, cfg Config) *node {
	return dir("meta-data",
		optional("ami-id", cfg.ImageID),
		leaf("ami-launch-index", "0"),
		leaf("hostname", id.IncusName),
		leaf("instance-action", "none"),
		leaf("instance-id", id.ContainerID),
		leaf("instance-life-cycle", "on-demand"),
		leaf("instance-type", instanceType(id)),
		leaf("local-hostname", id.IncusName),
		leaf("local-ipv4", id.IPv4),
		optional("mac", id.MAC),
		networkTree(id),
		dir("placement",
			leaf("availability-zone", cfg.Zone),
			leaf("region", cfg.Region),
		),
		optional("public-hostname", publicHostname(id, cfg)),
		optional("public-ipv4", firstNonEmpty(cfg.PublicIPs)),
		publicKeys(id),
		leaf("reservation-id", "r-"+id.ContainerID),
		leaf("security-groups", "default"),
		dir("services",
			leaf("domain", serviceDomain(cfg)),
			leaf("partition", partition),
		),
		svkexeTree(id, cfg),
	)
}

// networkTree mirrors EC2's per-interface tree, which tooling keys on the MAC.
// Without a MAC there is nothing to key on, so the whole subtree is left out
// rather than published under a placeholder.
func networkTree(id *Identity) *node {
	if id.MAC == "" {
		return nil
	}
	return dir("network",
		dir("interfaces",
			dir("macs",
				dir(id.MAC,
					leaf("device-number", "0"),
					leaf("interface-id", id.IncusName+"-eth0"),
					leaf("local-hostname", id.IncusName),
					leaf("local-ipv4s", id.IPv4),
					leaf("mac", id.MAC),
					leaf("owner-id", id.OwnerID),
				),
			),
		),
	)
}

// publicKeys publishes the owner's SSH keys under EC2's indexed layout, where
// the listing names each slot as "<index>=<name>".
func publicKeys(id *Identity) *node {
	if len(id.PublicKeys) == 0 {
		return nil
	}
	slots := make([]*node, 0, len(id.PublicKeys))
	for i, k := range id.PublicKeys {
		name := k.Name
		if name == "" {
			name = "key-" + strconv.Itoa(i)
		}
		slots = append(slots, &node{
			name:     strconv.Itoa(i),
			label:    strconv.Itoa(i) + "=" + name,
			children: []*node{leaf("openssh-key", k.Key)},
		})
	}
	return &node{name: "public-keys", children: slots}
}

// svkexeTree publishes what this platform knows that EC2 has no key for. It is
// the answer to "what is this VM for" that an agent or a provisioning script
// would otherwise have to be told out of band.
func svkexeTree(id *Identity, cfg Config) *node {
	return dir("svkexe",
		optional("agent-host", agentHost(id, cfg)),
		optional("aliases", strings.Join(id.Aliases, "\n")),
		leaf("app-port", strconv.Itoa(id.AppPort)),
		leaf("app-public", strconv.FormatBool(id.AppPublic)),
		leaf("container-name", id.Name),
		optional("created-at", timestamp(id.CreatedAt)),
		leaf("disk-gb", strconv.Itoa(id.DiskGB)),
		optional("gateway-domain", cfg.Domain),
		optional("initial-task", id.InitialTask),
		optional("initial-task-state", id.InitialTaskState),
		leaf("nesting", strconv.FormatBool(id.Nesting)),
		leaf("nesting-applied", strconv.FormatBool(id.NestingApplied)),
		leaf("owner-id", id.OwnerID),
	)
}

// identityDocument is EC2's instance identity document, minus the signatures: a
// signed document would claim a chain of trust this platform does not have, and
// publishing an unsigned one under a signature key would be worse than not
// publishing it at all.
func identityDocument(id *Identity, cfg Config) string {
	doc := struct {
		AccountID        string `json:"accountId"`
		Architecture     string `json:"architecture"`
		AvailabilityZone string `json:"availabilityZone"`
		ImageID          string `json:"imageId"`
		InstanceID       string `json:"instanceId"`
		InstanceType     string `json:"instanceType"`
		PendingTime      string `json:"pendingTime"`
		PrivateIP        string `json:"privateIp"`
		Region           string `json:"region"`
		Version          string `json:"version"`
	}{
		AccountID:        id.OwnerID,
		Architecture:     goruntime.GOARCH,
		AvailabilityZone: cfg.Zone,
		ImageID:          cfg.ImageID,
		InstanceID:       id.ContainerID,
		InstanceType:     instanceType(id),
		PendingTime:      timestamp(id.CreatedAt),
		PrivateIP:        id.IPv4,
		Region:           cfg.Region,
		Version:          documentVersion,
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		// Every field is a plain string; this cannot fail, and an empty object
		// is still valid JSON for a caller that parses the answer.
		return "{}"
	}
	return string(out)
}

// instanceType renders the VM's shape the way an EC2 instance type reads, so
// that code branching on it has something stable to branch on.
func instanceType(id *Identity) string {
	if id.CPULimit <= 0 || id.MemoryMB <= 0 {
		return typePrefix + ".custom"
	}
	return typePrefix + ".c" + strconv.Itoa(id.CPULimit) + "-m" + strconv.Itoa(id.MemoryMB)
}

func publicHostname(id *Identity, cfg Config) string {
	if cfg.Domain == "" {
		return ""
	}
	return id.Name + "." + cfg.Domain
}

func agentHost(id *Identity, cfg Config) string {
	if cfg.Domain == "" {
		return ""
	}
	return db.AgentHostPrefix + id.Name + "." + cfg.Domain
}

func serviceDomain(cfg Config) string {
	if cfg.Domain == "" {
		return internalDomain
	}
	return cfg.Domain
}

func firstNonEmpty(values []string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func timestamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
