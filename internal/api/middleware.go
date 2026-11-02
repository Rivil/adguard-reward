package api

import "net/http"

// CSRFHeader and CSRFValue are the custom header every state-changing
// request must carry. A browser cannot attach a custom header cross-origin
// without a CORS preflight, which the app never grants, so its presence
// proves the request came from the SPA.
const (
	CSRFHeader = "X-Requested-With"
	CSRFValue  = "adguard-reward"
)

// noStore marks every response under the API uncacheable. The header is set
// before next runs so 4xx/5xx paths and the mux's own 404/405 carry it too.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// csrf rejects any non-safe method without the exact CSRF header, before
// the route is even matched.
func csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			if r.Header.Get(CSRFHeader) != CSRFValue {
				writeError(w, http.StatusForbidden, CodeForbidden, "missing "+CSRFHeader+" header")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
