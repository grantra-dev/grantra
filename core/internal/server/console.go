package server

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// consoleCSP lets the console load its own scripts, styles and data and
// nothing else. The Vite build emits no inline scripts or styles.
// 'wasm-unsafe-eval' lets the session player compile its terminal emulator
// (WebAssembly); it does not allow eval of JavaScript.
const consoleCSP = "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"

// consoleHandler serves the built React console. Files that exist are served
// as they are; any other path without a file extension is a console route and
// gets index.html, so that reloading /my-access works.
func consoleHandler(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Registered without a method so that more specific paths such as
		// /api/ win over it; only reads are served here.
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Security-Policy", consoleCSP)
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if st, err := fs.Stat(ui, name); err == nil && !st.IsDir() {
				// Vite puts a content hash in every file name under assets/.
				if strings.HasPrefix(name, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, ui, "index.html")
	})
}
