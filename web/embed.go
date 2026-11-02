// Package web embeds the built frontend (web/dist) into the binary.
//
// The pattern needs at least one file under dist to compile, so a tracked
// dist/.gitkeep keeps go vet and go test working on a fresh clone before
// make build-web has run; the spa handler answers 503 for such a build.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the built SPA rooted at what was web/dist.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: embedded dist dir missing: " + err.Error())
	}
	return sub
}
