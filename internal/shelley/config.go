package shelley

import "fmt"

const (
	// Port is the port Shelley listens on inside the container.
	Port = 9000

	// RequireHeader is the HTTP header Shelley requires for user identification.
	RequireHeader = "X-ExeDev-Userid"

	// DBPath is where Shelley stores its SQLite database inside the container.
	DBPath = "/data/shelley.db"

	// DefaultImage is the base container image used for Shelley containers.
	DefaultImage = "svkexe-base"

	// EnvFilePath is where materialized env vars are written inside the container.
	EnvFilePath = "/etc/shelley/env"

	// LLMEnvFilePath is the optional env file for LLM proxy configuration.
	LLMEnvFilePath = "/etc/shelley/llm-proxy.env"

	// ContainerUser is the non-root user inside svkexe containers.
	ContainerUser = "user"
)

// LLMProxyConfig holds the gateway-level LLM proxy settings to pass to Shelley.
type LLMProxyConfig struct {
	// BaseURL is the OpenAI-compatible base URL (e.g. "https://svk.bar/api/llm/v1").
	BaseURL string
	// Token is the Bearer token Shelley uses to authenticate to the proxy.
	Token string
}

// SystemdUnitContent returns the content of the systemd unit file for Shelley.
func SystemdUnitContent() string {
	return fmt.Sprintf(`[Unit]
Description=Shelley LLM execution service
After=network.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=/home/%s
EnvironmentFile=%s
EnvironmentFile=-%s
ExecStart=/usr/local/bin/shelley --port %d --db %s --require-header %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, ContainerUser, ContainerUser, ContainerUser, EnvFilePath, LLMEnvFilePath, Port, DBPath, RequireHeader)
}
