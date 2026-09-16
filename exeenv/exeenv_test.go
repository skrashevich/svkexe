package exeenv

import (
	"strings"
	"testing"
)

func TestNewBuildsConfiguredEnvironment(t *testing.T) {
	env, err := New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	if got := env.ReflectionURL(); got != "https://reflection.int.example.test" {
		t.Fatalf("ReflectionURL() = %q", got)
	}
	if got := env.IntegrationURL("llm", true); got != "https://llm.team.example.test" {
		t.Fatalf("IntegrationURL() = %q", got)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		scheme  string
		boxHost string
		wantErr string
	}{
		{name: "missing scheme", boxHost: "example.test", wantErr: "scheme"},
		{name: "unsupported scheme", scheme: "ftp", boxHost: "example.test", wantErr: "scheme"},
		{name: "missing box host", scheme: "https", wantErr: "box_host"},
		{name: "URL as box host", scheme: "https", boxHost: "https://example.test", wantErr: "box_host"},
		{name: "path in box host", scheme: "https", boxHost: "example.test/path", wantErr: "box_host"},
		{name: "port in box host", scheme: "https", boxHost: "example.test:443", wantErr: "box_host"},
		{name: "space in box host", scheme: "https", boxHost: "example test", wantErr: "box_host"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.scheme, tt.boxHost)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("New(%q, %q) error = %v, want error containing %q", tt.scheme, tt.boxHost, err, tt.wantErr)
			}
		})
	}
}

func TestCurrentPrefersConfiguredEnvironment(t *testing.T) {
	old := configured.Load()
	t.Cleanup(func() { configured.Store(old) })

	env, err := New("https", "example.test")
	if err != nil {
		t.Fatal(err)
	}
	Configure(env)

	got, err := Current()
	if err != nil {
		t.Fatal(err)
	}
	if got.ReflectionURL() != env.ReflectionURL() {
		t.Fatalf("Current().ReflectionURL() = %q, want %q", got.ReflectionURL(), env.ReflectionURL())
	}
}

func TestFromHostnameBuildsEnvironmentURLs(t *testing.T) {
	tests := []struct {
		name           string
		hostname       string
		reflectionURL  string
		personalLLMURL string
		teamLLMURL     string
	}{
		{
			name:           "production",
			hostname:       "box.exe.xyz",
			reflectionURL:  "https://reflection.int.exe.xyz",
			personalLLMURL: "https://llm.int.exe.xyz",
			teamLLMURL:     "https://llm.team.exe.xyz",
		},
		{
			name:           "development",
			hostname:       "box.exe.cloud",
			reflectionURL:  "http://reflection.int.exe.cloud",
			personalLLMURL: "http://llm.int.exe.cloud",
			teamLLMURL:     "http://llm.team.exe.cloud",
		},
		{
			name:           "development subdomain",
			hostname:       "box.shelley.exe.cloud",
			reflectionURL:  "http://reflection.int.exe.cloud",
			personalLLMURL: "http://llm.int.exe.cloud",
			teamLLMURL:     "http://llm.team.exe.cloud",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := FromHostname(tt.hostname)
			if got := env.ReflectionURL(); got != tt.reflectionURL {
				t.Errorf("ReflectionURL() = %q, want %q", got, tt.reflectionURL)
			}
			if got := env.IntegrationURL("llm", false); got != tt.personalLLMURL {
				t.Errorf("IntegrationURL(personal) = %q, want %q", got, tt.personalLLMURL)
			}
			if got := env.IntegrationURL("llm", true); got != tt.teamLLMURL {
				t.Errorf("IntegrationURL(team) = %q, want %q", got, tt.teamLLMURL)
			}
		})
	}
}
