// Package spa serves the built frontend from an fs.FS with single-page-app
// fallback: a client route with no file behind it gets index.html, a missing
// asset (anything with an extension) is a plain 404, and /api/* and /healthz
// are never answered with HTML so a stale service worker or a typo'd route
// cannot masquerade as a JSON endpoint.
package spa

import (
	"bytes"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// kind classifies what a request path resolves to.
type kind int

const (
	kindNotFound  kind = iota // missing extensioned path: plain 404
	kindAPI                   // /api/* or /healthz: JSON 404, never HTML
	kindNotBuilt              // index.html absent: 503 pointing at make build-web
	kindNoCache               // index.html, sw.js, manifest: revalidate every load
	kindImmutable             // hashed asset under /assets/: cache forever
	kindStatic                // anything else that exists: cache an hour
)

const (
	indexFile   = "index.html"
	cacheNo     = "no-cache"
	cacheForev  = "public, max-age=31536000, immutable"
	cacheHour   = "public, max-age=3600"
	notBuiltMsg = "frontend not built: run make build-web"
)

// noCacheFiles are re-fetched every load: the shell so a deploy shows up, the
// worker so the browser's update check sees a new one, the manifest so an
// icon or name change lands.
var noCacheFiles = map[string]bool{indexFile: true, "sw.js": true, "manifest.webmanifest": true}

// resolve maps a URL path onto a file in fsys and how to cache it. A dotfile
// or a directory counts as missing; a missing path with an extension is a
// 404 and one without falls back to index.html.
func resolve(fsys fs.FS, urlPath string) (file string, k kind) {
	p := path.Clean("/" + urlPath)
	if p == "/healthz" || p == "/api" || strings.HasPrefix(p, "/api/") {
		return "", kindAPI
	}
	name := strings.TrimPrefix(p, "/")
	if name == "" {
		name = indexFile
	}
	base := path.Base(name)
	if !strings.HasPrefix(base, ".") && isFile(fsys, name) {
		return name, fileKind(name)
	}
	if name != indexFile && path.Ext(base) != "" {
		return "", kindNotFound
	}
	if isFile(fsys, indexFile) {
		return indexFile, kindNoCache
	}
	return "", kindNotBuilt
}

func isFile(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

func fileKind(name string) kind {
	switch {
	case noCacheFiles[name]:
		return kindNoCache
	case strings.HasPrefix(name, "assets/"):
		return kindImmutable
	default:
		return kindStatic
	}
}

// Handler serves fsys with SPA fallback. GET and HEAD only.
func Handler(fsys fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if strings.Contains(r.URL.Path, "..") {
			http.Error(w, "bad path", http.StatusBadRequest)
			return
		}
		file, k := resolve(fsys, r.URL.Path)
		switch k {
		case kindAPI:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":"not_found"}`+"\n")
		case kindNotFound:
			http.NotFound(w, r)
		case kindNotBuilt:
			http.Error(w, notBuiltMsg, http.StatusServiceUnavailable)
		default:
			serveFile(w, r, fsys, file, k)
		}
	})
}

// serveFile writes one file through ServeContent so HEAD, ranges and
// conditional requests behave, with the cache policy the kind dictates. The
// body goes through memory rather than ServeFileFS because the latter
// redirects a literal /index.html request to ./, which a fallback must not.
func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string, k kind) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		http.Error(w, "read "+name+": "+err.Error(), http.StatusInternalServerError)
		return
	}
	var modTime time.Time
	if info, err := fs.Stat(fsys, name); err == nil {
		modTime = info.ModTime()
	}
	switch k {
	case kindNoCache:
		w.Header().Set("Cache-Control", cacheNo)
	case kindImmutable:
		w.Header().Set("Cache-Control", cacheForev)
	default:
		w.Header().Set("Cache-Control", cacheHour)
	}
	if strings.HasSuffix(name, ".webmanifest") {
		w.Header().Set("Content-Type", "application/manifest+json")
	}
	http.ServeContent(w, r, name, modTime, bytes.NewReader(data))
}
