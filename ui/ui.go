// Package ui embeds the dashboard HTML templates.
package ui

import "embed"

//go:embed templates/*.html
var Templates embed.FS
