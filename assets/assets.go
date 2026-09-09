// Package assets embeds static resources (the tray icon) into the binary.
package assets

import _ "embed"

// Icon is the hlauncher application/tray icon (.ico bytes).
//
//go:embed hlauncher.ico
var Icon []byte
