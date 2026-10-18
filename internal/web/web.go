// Package web serves the built React app embedded in the binary, so one
// process serves the UI, the API and the WebSocket from a single origin.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist holds the Vite build output (npm run build writes it here). It always
// contains .gitkeep, so the package compiles before the UI is built.
//
//go:embed all:dist
var dist embed.FS

// Handler serves static assets and falls back to index.html for client-side
// routes such as /trade/ATHR.
func Handler() http.Handler {
	files, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err)
	}
	return handlerFor(files)
}

func handlerFor(files fs.FS) http.Handler {
	index, indexErr := fs.ReadFile(files, "index.html")
	assets := http.FileServer(http.FS(files))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name != "" && name != "index.html" {
			if info, err := fs.Stat(files, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					// Vite fingerprints asset names, so they never change.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				assets.ServeHTTP(w, r)
				return
			}
			if path.Ext(name) != "" {
				http.NotFound(w, r)
				return
			}
		}
		if indexErr != nil {
			http.Error(w, "The web UI is not built. Run `npm run build`, then rebuild the server.", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	})
}
