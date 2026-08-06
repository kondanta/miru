// Package web embeds the compiled Vue 3 SPA and exposes it as an fs.FS.
// The dist directory is populated by `mise run build-ui`. When dist has not
// been built, DistDirFS contains only the placeholder .gitkeep and the server
// will serve an empty shell.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// DistDirFS is the embedded Vue UI rooted at the dist directory.
// Import this from internal/server to serve the frontend.
var DistDirFS = mustSubFS(dist, "dist")

func mustSubFS(f embed.FS, dir string) fs.FS {
	sub, err := fs.Sub(f, dir)
	if err != nil {
		panic("web: failed to sub embedded FS at " + dir + ": " + err.Error())
	}
	return sub
}
