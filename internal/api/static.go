package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// staticFS serves the embedded web UI. Files are loaded and gzip-compressed
// once at startup; unknown paths without an extension fall back to
// index.html for client-side routing.
type staticFS struct {
	files map[string]*asset
}

type asset struct {
	data, gz []byte
	ctype    string
	etag     string
}

var compressible = map[string]bool{".html": true, ".js": true, ".css": true, ".svg": true, ".json": true, ".txt": true, ".map": true, ".webmanifest": true}

func newStaticFS(fsys fs.FS) *staticFS {
	s := &staticFS{files: make(map[string]*asset)}
	if fsys == nil {
		return s
	}
	fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return nil
		}
		ext := path.Ext(p)
		sum := sha256.Sum256(data)
		a := &asset{data: data, ctype: mime.TypeByExtension(ext), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		if a.ctype == "" {
			a.ctype = "application/octet-stream"
		}
		if compressible[ext] && len(data) > 1024 {
			var buf bytes.Buffer
			zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			zw.Write(data)
			zw.Close()
			if buf.Len() < len(data) {
				a.gz = buf.Bytes()
			}
		}
		s.files["/"+p] = a
		return nil
	})
	slog.Debug("web UI assets loaded", "files", len(s.files))
	return s
}

func (s *staticFS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := path.Clean("/" + r.URL.Path)
	a := s.files[p]
	immutable := strings.HasPrefix(p, "/assets/")
	if a == nil {
		if path.Ext(p) != "" && p != "/" {
			http.NotFound(w, r)
			return
		}
		if a = s.files["/index.html"]; a == nil {
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("FireGateway web UI is not built into this binary. Build it with `make build`, or use the API under /api.\n"))
			return
		}
		immutable = false
	}

	h := w.Header()
	h.Set("Content-Type", a.ctype)
	h.Set("ETag", a.etag)
	h.Add("Vary", "Accept-Encoding")
	if immutable {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	if r.Header.Get("If-None-Match") == a.etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := a.data
	if a.gz != nil && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		h.Set("Content-Encoding", "gzip")
		body = a.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method == http.MethodHead {
		return
	}
	w.Write(body)
}
