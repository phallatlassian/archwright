package works

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The dashboard's pages are plain HTML, CSS and JavaScript embedded in the
// binary (ADR 0016); nothing is fetched at runtime and there is no build step.
//
//go:embed ui
var uiFS embed.FS

// withUI serves the dashboard's static files at / and /ui/ and everything else
// through api. The files carry no data and no secret, so a browser can load
// them without the token it cannot send on a navigation; the Host check still
// applies. The page reads the token from the URL fragment, which the browser
// never sends to a server, and presents it on every API call (ADR 0007).
func withUI(api http.Handler) http.Handler {
	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		static := r.URL.Path == "/" || strings.HasPrefix(r.URL.Path, "/ui/")
		if !static || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			api.ServeHTTP(w, r)
			return
		}
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		if r.URL.Path != "/" { // the file server serves a directory's index.html itself
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/ui")
		}
		files.ServeHTTP(w, r)
	})
}
