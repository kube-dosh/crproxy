// Package web embeds the production management console.
package web

import (
	"bytes"
	"embed"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
)

//go:embed all:dist
var assets embed.FS

var content fs.FS

func init() {
	var err error
	content, err = fs.Sub(assets, "dist")
	if err != nil {
		panic("embedded web assets are unavailable: " + err.Error())
	}
}

func Handler() http.Handler {
	return http.HandlerFunc(serve)
}

func serve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/metrics" || strings.HasPrefix(r.URL.Path, "/v2/") || r.URL.Path == "/v2" {
		http.NotFound(w, r)
		return
	}
	requestPath := path.Clean("/" + r.URL.Path)
	if requestPath != "/" {
		requestPath = strings.TrimPrefix(requestPath, "/")
		if file, err := content.Open(requestPath); err == nil {
			info, statErr := file.Stat()
			_ = file.Close()
			if statErr == nil && !info.IsDir() {
				serveFile(w, r, requestPath, info)
				return
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
	}
	if requestPath != "/" && path.Ext(requestPath) != "" {
		http.NotFound(w, r)
		return
	}
	serveIndex(w, r)
}

func serveFile(w http.ResponseWriter, r *http.Request, name string, info fs.FileInfo) {
	data, err := fs.ReadFile(content, name)
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if name == "index.html" {
		setHTMLHeaders(w)
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(data))
		return
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=300")
	}
	if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	http.ServeContent(w, r, name, info.ModTime(), bytes.NewReader(data))
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(content, "index.html")
	if err != nil {
		http.Error(w, "web console is not built", http.StatusServiceUnavailable)
		return
	}
	setHTMLHeaders(w)
	http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(data))
}

func setHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
}
