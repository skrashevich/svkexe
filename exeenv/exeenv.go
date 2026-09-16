// Package exeenv resolves exe.dev environment-specific service URLs.
package exeenv

import (
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
)

// Environment describes the scheme and base domain used by exe.dev services.
type Environment struct {
	scheme  string
	boxHost string
}

// New returns an environment for a configured scheme and box hostname.
func New(scheme, boxHost string) (Environment, error) {
	if scheme != "http" && scheme != "https" {
		return Environment{}, fmt.Errorf("invalid exe environment scheme %q: must be http or https", scheme)
	}
	parsed, err := url.Parse(scheme + "://" + boxHost)
	if err != nil || boxHost == "" || parsed.Host != boxHost || parsed.Hostname() != boxHost || parsed.Port() != "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Environment{}, fmt.Errorf("invalid exe environment box_host %q: must be a bare hostname", boxHost)
	}
	return Environment{scheme: scheme, boxHost: boxHost}, nil
}

// FromHostname resolves the exe.dev environment containing hostname.
func FromHostname(hostname string) Environment {
	if strings.Contains(strings.ToLower(hostname), "exe.cloud") {
		return Environment{scheme: "http", boxHost: "exe.cloud"}
	}
	return Environment{scheme: "https", boxHost: "exe.xyz"}
}

var current = sync.OnceValues(func() (Environment, error) {
	out, err := exec.Command("hostname", "-f").Output()
	if err != nil {
		return Environment{}, fmt.Errorf("resolve qualified hostname: %w", err)
	}
	hostname := strings.TrimSpace(string(out))
	if hostname == "" {
		return Environment{}, fmt.Errorf("resolve qualified hostname: empty output")
	}
	return FromHostname(hostname), nil
})

var configured atomic.Pointer[Environment]

// Configure sets the process-wide exe.dev environment used by Current.
func Configure(env Environment) {
	configured.Store(&env)
}

// Current resolves the environment from this machine's qualified hostname.
func Current() (Environment, error) {
	if env := configured.Load(); env != nil {
		return *env, nil
	}
	return current()
}

// ReflectionURL returns the reflection integration's base URL.
func (e Environment) ReflectionURL() string {
	return e.scheme + "://reflection.int." + e.boxHost
}

// IntegrationHost returns the hostname for a personal or team integration.
func (e Environment) IntegrationHost(name string, team bool) string {
	scope := "int"
	if team {
		scope = "team"
	}
	return name + "." + scope + "." + e.boxHost
}

// IntegrationURL returns the base URL for a personal or team integration.
func (e Environment) IntegrationURL(name string, team bool) string {
	return e.scheme + "://" + e.IntegrationHost(name, team)
}
