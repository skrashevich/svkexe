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
)

// SystemdUnitContent returns the content of the systemd unit file for Shelley.
func SystemdUnitContent() string {
	return fmt.Sprintf(`[Unit]
Description=Shelley LLM execution service
After=network.target

[Service]
Type=simple
EnvironmentFile=%s
ExecStart=/usr/local/bin/shelley --port %d --db %s --require-header %s
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`, EnvFilePath, Port, DBPath, RequireHeader)
}
