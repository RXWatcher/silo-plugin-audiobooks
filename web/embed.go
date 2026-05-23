// Package web embeds the built SPA from web/dist. The embed sits in web/ so
// it can reach the sibling dist/ directory; //go:embed is constrained to the
// package directory and its descendants.
package web

import (
	"embed"
	"html"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// FSEmbed returns the embedded SPA as an fs.FS rooted at dist/.
func FSEmbed() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic("web: " + err.Error())
	}
	return sub
}

// FS returns the SPA file system rooted at dist/.
func FS() http.FileSystem { return http.FS(FSEmbed()) }

// SPAHandler returns an http.Handler that serves the embedded SPA. For paths
// that don't match a real file, it serves index.html so client-side routing
// works.
//
// Static-file content types are set explicitly because plugins run in a
// minimal container with no /etc/mime.types — Go's mime database falls back
// to text/plain for anything outside the small builtin table (notably
// .webmanifest), and downstream the silo proxy adds
// X-Content-Type-Options: nosniff, so a wrong type makes the browser refuse
// to register the service worker.
var pwaContentTypes = map[string]string{
	"/manifest.webmanifest": "application/manifest+json; charset=utf-8",
	"/sw.js":                "application/javascript; charset=utf-8",
	"/icon.svg":             "image/svg+xml",
	"/icon-192.png":         "image/png",
	"/icon-512.png":         "image/png",
	"/apple-touch-icon.png": "image/png",
}

func SPAHandler() http.Handler {
	fileSrv := http.FileServer(FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isSPARequest(r.URL.Path) {
			serveIndex(w, r)
			return
		}
		if ct, ok := pwaContentTypes[r.URL.Path]; ok {
			w.Header().Set("Content-Type", ct)
		}
		f, err := FS().Open(r.URL.Path)
		if err != nil {
			r.URL.Path = "/"
		} else {
			_ = f.Close()
		}
		fileSrv.ServeHTTP(w, r)
	})
}

func isSPARequest(path string) bool {
	return path == "/" || path == "/admin" || path == "/admin/" || strings.HasPrefix(path, "/admin/")
}

func serveIndex(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(FSEmbed(), "index.html")
	if err != nil {
		http.Error(w, "spa not available", http.StatusServiceUnavailable)
		return
	}
	htmlBody := string(data)
	if strings.HasPrefix(r.URL.Path, "/admin") {
		prefix := adminAssetPrefix(r.URL.Path)
		htmlBody = strings.ReplaceAll(htmlBody, `src="./assets/`, `src="`+prefix)
		htmlBody = strings.ReplaceAll(htmlBody, `href="./assets/`, `href="`+prefix)
	}
	theme := r.URL.Query().Get("theme")
	if theme == "" {
		theme = r.Header.Get("X-Silo-Theme")
	}
	if theme == "" {
		theme = r.Header.Get("X-Silo-User-Theme")
	}
	if theme != "" {
		safe := html.EscapeString(theme)
		if strings.Contains(htmlBody, `<html lang="en">`) {
			htmlBody = strings.Replace(htmlBody, `<html lang="en">`, `<html lang="en" data-theme="`+safe+`">`, 1)
		} else if strings.Contains(htmlBody, `<html`) {
			htmlBody = strings.Replace(htmlBody, `<html`, `<html data-theme="`+safe+`"`, 1)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(htmlBody))
}

func adminAssetPrefix(requestPath string) string {
	if requestPath == "/admin" || requestPath == "/" {
		return "assets/"
	}
	return "../assets/"
}
