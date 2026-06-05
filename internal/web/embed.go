// Package web embeds the built UI assets so konflate ships as a single binary.
// The npm build writes content-hashed bundles and the hash-referencing
// index.html into dist/ (see vite.config.ts). Only the static favicon is
// committed; it anchors the embed below so the Go binary builds even without a
// prior UI build (the UI just isn't served until one runs).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the UI filesystem rooted at dist (so index.html is at "/").
func FS() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at compile time; Sub cannot fail
	}
	return sub
}
