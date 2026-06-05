// Package web embeds the built UI assets so konflate ships as a single binary.
// The npm build writes hashed bundles into dist/; a placeholder index.html is
// committed so the Go binary builds even without a prior UI build.
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
