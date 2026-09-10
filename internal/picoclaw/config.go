package picoclaw

import "fmt"

const (
	// Port is the port PicoClaw listens on inside the container.
	Port = 9000

	// RequireHeader is the HTTP header PicoClaw requires for user identification.
	RequireHeader = "X-ExeDev-Userid"

	// DBPath holds conversations and models. The schema is still the one the
	// preserved application shell created, but the name follows the runtime.
	DBPath = "/data/picoclaw.db"

	// LegacyDBPath is the pre-rename location, migrated on setup.
	LegacyDBPath = "/data/shelley.db"

	// DefaultImage is the base container image used for PicoClaw containers.
	DefaultImage = "svkexe-base"

	// ConfigDir holds the agent configuration and materialized credentials.
	ConfigDir = "/etc/picoclaw"

	// LegacyConfigDir is the pre-rename location, migrated on setup.
	LegacyConfigDir = "/etc/shelley"

	// EnvFilePath is where materialized env vars are written inside the container.
	EnvFilePath = ConfigDir + "/env"

	// ConfigFilePath is the agent configuration file.
	ConfigFilePath = ConfigDir + "/picoclaw.json"

	// ContainerUser is the non-root user inside svkexe containers.
	ContainerUser = "user"

	// gatewayModelPrefix marks models seeded from the deployment-wide
	// OPENROUTER_MODELS list, which route through the gateway's own key.
	gatewayModelPrefix = "svkexe-"

	// userModelPrefix marks models seeded from the owner's own LLM keys.
	userModelPrefix = "svkexe_user:"
)

// LLMProxyConfig holds the gateway-level LLM proxy settings to pass to PicoClaw.
type LLMProxyConfig struct {
	// BaseURL is the LLM gateway URL (e.g. "https://svk.bar/api/llm/v1").
	BaseURL string
	// Token is the Bearer token PicoClaw uses to authenticate to the proxy.
	Token string
	// Models is the list of OpenRouter model IDs (e.g. ["anthropic/claude-sonnet-4", "openai/gpt-4o"]).
	Models []string
}

// SystemdUnitContent returns the content of the systemd unit file for PicoClaw.
func SystemdUnitContent() string {
	return fmt.Sprintf(`[Unit]
Description=PicoClaw LLM execution service
After=network.target

[Service]
Type=simple
User=%s
Group=%s
WorkingDirectory=/home/%s
EnvironmentFile=%s
ExecStart=/usr/local/bin/picoclaw --config %s -db %s serve -port %d -require-header %s -banner "PicoClaw · svkexe"
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, ContainerUser, ContainerUser, ContainerUser, EnvFilePath, ConfigFilePath, DBPath, Port, RequireHeader)
}
