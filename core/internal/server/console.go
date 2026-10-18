package server

import (
	"bytes"
	"compress/gzip"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"
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
	zipped := &gzipCache{ui: ui}
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
					if zipped.serve(w, r, name) {
						return
					}
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

// gzipCache compresses the console's scripts and styles once, the first time
// each is asked for, and keeps them: the files are built into the binary and
// never change while it runs. The main script shrinks to about a quarter,
// which is what a first visit over a slow link waits for. Grantra often runs
// without a proxy in front that would compress for it.
type gzipCache struct {
	ui    fs.FS
	files sync.Map // name -> []byte, nil when compressing does not pay
}

// compressible are the asset types worth compressing; images and fonts
// already are.
var compressible = map[string]bool{".js": true, ".css": true, ".svg": true, ".json": true, ".wasm": true, ".map": true}

// serve writes the compressed file and reports true, or reports false so the
// caller serves it as it is.
func (c *gzipCache) serve(w http.ResponseWriter, r *http.Request, name string) bool {
	ext := path.Ext(name)
	if !compressible[ext] {
		return false
	}
	w.Header().Add("Vary", "Accept-Encoding")
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) || r.Header.Get("Range") != "" {
		return false
	}
	body := c.get(name)
	if body == nil {
		return false
	}
	h := w.Header()
	if ct := mime.TypeByExtension(ext); ct != "" {
		h.Set("Content-Type", ct)
	}
	h.Set("Content-Encoding", "gzip")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
	return true
}

func (c *gzipCache) get(name string) []byte {
	if v, ok := c.files.Load(name); ok {
		return v.([]byte)
	}
	raw, err := fs.ReadFile(c.ui, name)
	if err != nil {
		return nil
	}
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	var body []byte
	// Tiny files can grow; those are served as they are.
	if buf.Len() < len(raw) {
		body = buf.Bytes()
	}
	v, _ := c.files.LoadOrStore(name, body)
	return v.([]byte)
}

// acceptsGzip reads an Accept-Encoding header: an explicit gzip wins over
// "*", and a q of zero ("gzip;q=0") is a no.
func acceptsGzip(header string) bool {
	star := false
	for _, part := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		coding = strings.TrimSpace(coding)
		q := strings.TrimSpace(params)
		zero := strings.HasPrefix(q, "q=0") && strings.Trim(strings.TrimPrefix(q, "q="), "0.") == ""
		switch {
		case strings.EqualFold(coding, "gzip"):
			return !zero
		case coding == "*":
			star = !zero
		}
	}
	return star
}
