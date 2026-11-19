package main

import (
	"embed"
	"io/fs"
)

//go:embed all:assets
var embeddedAssets embed.FS

// webAssets prefers the built Web UI copied in by `make desktop` and falls back to a
// placeholder page so a plain `go build` still produces a runnable shell.
func webAssets() fs.FS {
	if dist, err := fs.Sub(embeddedAssets, "assets/dist"); err == nil {
		if _, statErr := fs.Stat(dist, "index.html"); statErr == nil {
			return dist
		}
	}
	placeholder, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("desktop assets missing: " + err.Error())
	}
	return placeholder
}
