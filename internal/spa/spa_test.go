package spa

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

const indexHTML = "<!doctype html><html><head><title>adguard-reward</title></head><body>shell</body></html>\n"

func dist() fstest.MapFS {
	return fstest.MapFS{
		"index.html":           {Data: []byte(indexHTML)},
		"assets/app-abc.js":    {Data: []byte("console.log('app')")},
		"sw.js":                {Data: []byte("self.addEventListener('fetch', () => {})")},
		"manifest.webmanifest": {Data: []byte(`{"name":"adguard-reward"}`)},
		"icon-192.png":         {Data: []byte{0x89, 'P', 'N', 'G'}},
		".gitkeep":             {Data: nil},
	}
}

func get(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func TestResolve_Table(t *testing.T) {
	fsys := dist()
	cases := []struct {
		path string
		file string
		kind kind
	}{
		{"/", "index.html", kindNoCache},
		{"/index.html", "index.html", kindNoCache},
		{"/children", "index.html", kindNoCache},
		{"/buttons/deep/path", "index.html", kindNoCache},
		{"/assets/app-abc.js", "assets/app-abc.js", kindImmutable},
		{"/assets/gone.js", "", kindNotFound},
		{"/favicon.ico", "", kindNotFound},
		{"/old-hash.js", "", kindNotFound},
		{"/.gitkeep", "", kindNotFound},
		{"/.env", "", kindNotFound},
		{"/api/v2/x", "", kindAPI},
		{"/api/", "", kindAPI},
		{"/healthz", "", kindAPI},
		{"/sw.js", "sw.js", kindNoCache},
		{"/manifest.webmanifest", "manifest.webmanifest", kindNoCache},
		{"/icon-192.png", "icon-192.png", kindStatic},
		{"/assets/", "index.html", kindNoCache},
		{"/assets", "index.html", kindNoCache},
	}
	for _, c := range cases {
		file, k := resolve(fsys, c.path)
		if file != c.file || k != c.kind {
			t.Errorf("resolve(%q) = (%q, %d), want (%q, %d)", c.path, file, k, c.file, c.kind)
		}
	}
}

func TestSPA_Fallback(t *testing.T) {
	h := Handler(dist())
	for _, p := range []string{"/buttons", "/children/7"} {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET %s Content-Type = %q, want text/html", p, ct)
		}
		if rec.Body.String() != indexHTML {
			t.Fatalf("GET %s body = %q, want index.html", p, rec.Body.String())
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Fatalf("GET %s Cache-Control = %q, want no-cache", p, cc)
		}
	}
	rec := get(t, h, http.MethodGet, "/assets/app-abc.js")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Fatalf("GET /assets/app-abc.js = %d %q, want 200 immutable", rec.Code, rec.Header().Get("Cache-Control"))
	}
	rec = get(t, h, http.MethodGet, "/assets/")
	if rec.Code != http.StatusOK || rec.Body.String() != indexHTML {
		t.Fatalf("GET /assets/ = %d %q, want 200 index.html (no listing)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "app-abc.js") {
		t.Fatalf("GET /assets/ body lists directory contents: %q", rec.Body.String())
	}
}

func TestSPA_NeverHTMLForAPI(t *testing.T) {
	h := Handler(dist())
	for _, p := range []string{"/api/v2/x", "/api/v1/", "/healthz"} {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
			t.Fatalf("GET %s Content-Type = %q, want application/json", p, ct)
		}
		if body := strings.TrimSpace(rec.Body.String()); body != `{"error":"not_found"}` {
			t.Fatalf("GET %s body = %q, want {\"error\":\"not_found\"}", p, body)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
			t.Fatalf("GET %s answered with HTML", p)
		}
	}
}

func TestHandler_MissingAssetIs404(t *testing.T) {
	h := Handler(dist())
	for _, p := range []string{"/assets/old-hash.js", "/favicon.ico", "/.gitkeep"} {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want 404", p, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") {
			t.Fatalf("GET %s Content-Type = %q, want not text/html", p, ct)
		}
		if strings.Contains(strings.ToLower(rec.Body.String()), "<html") {
			t.Fatalf("GET %s fell back to the shell", p)
		}
	}
}

func TestSPA_NotBuilt(t *testing.T) {
	for name, fsys := range map[string]fs.FS{
		"gitkeep only": fstest.MapFS{".gitkeep": {}},
		"empty":        fstest.MapFS{},
	} {
		h := Handler(fsys)
		for _, p := range []string{"/", "/index.html", "/children"} {
			rec := get(t, h, http.MethodGet, p)
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("%s: GET %s = %d, want 503", name, p, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), "make build-web") {
				t.Fatalf("%s: GET %s body = %q, want to name make build-web", name, p, rec.Body.String())
			}
		}
	}
}

func TestSPA_CacheHeaders(t *testing.T) {
	h := Handler(dist())
	want := map[string]string{
		"/sw.js":                "no-cache",
		"/manifest.webmanifest": "no-cache",
		"/icon-192.png":         "public, max-age=3600",
		"/assets/app-abc.js":    "public, max-age=31536000, immutable",
	}
	for p, cc := range want {
		rec := get(t, h, http.MethodGet, p)
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != cc {
			t.Fatalf("GET %s = %d %q, want 200 %q", p, rec.Code, rec.Header().Get("Cache-Control"), cc)
		}
		head := get(t, h, http.MethodHead, p)
		if head.Code != http.StatusOK || head.Header().Get("Cache-Control") != cc {
			t.Fatalf("HEAD %s = %d %q, want 200 %q", p, head.Code, head.Header().Get("Cache-Control"), cc)
		}
		if head.Header().Get("Content-Type") != rec.Header().Get("Content-Type") {
			t.Fatalf("HEAD %s Content-Type = %q, GET gave %q", p, head.Header().Get("Content-Type"), rec.Header().Get("Content-Type"))
		}
		if head.Body.Len() != 0 {
			t.Fatalf("HEAD %s carried a %d-byte body", p, head.Body.Len())
		}
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		rec := get(t, h, m, "/")
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s / = %d, want 405", m, rec.Code)
		}
		if allow := rec.Header().Get("Allow"); !strings.Contains(allow, "GET") {
			t.Fatalf("%s / Allow = %q, want to contain GET", m, allow)
		}
	}
}

func TestSPA_ManifestType(t *testing.T) {
	h := Handler(dist())
	rec := get(t, h, http.MethodGet, "/manifest.webmanifest")
	if ct := rec.Header().Get("Content-Type"); ct != "application/manifest+json" {
		t.Fatalf("manifest Content-Type = %q, want application/manifest+json", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"name":"adguard-reward"}` {
		t.Fatalf("manifest body = %q", body)
	}
	rec = get(t, h, http.MethodGet, "/")
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("GET / Content-Type = %q, want text/html*", ct)
	}
}

func TestSPA_NoListing(t *testing.T) {
	h := Handler(dist())
	for _, p := range []string{"/../embed.go", "/assets/../index.html", "/assets/../../etc/passwd"} {
		rec := get(t, h, http.MethodGet, p)
		switch rec.Code {
		case http.StatusOK:
			if rec.Body.String() != indexHTML {
				t.Fatalf("GET %s = 200 with body %q, want index.html", p, rec.Body.String())
			}
		case http.StatusBadRequest:
		default:
			t.Fatalf("GET %s = %d, want 200 index.html or 400", p, rec.Code)
		}
	}
}
