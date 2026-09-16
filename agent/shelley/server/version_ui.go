package server

import (
	"shelley.exe.dev/ui"
	"shelley.exe.dev/version"
)

func init() {
	version.RegisterBuildInfoFS(ui.Dist)
}
