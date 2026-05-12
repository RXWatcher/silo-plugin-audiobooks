// Package web embeds the built SPA from web/dist. The embed sits in web/ so
// it can reach the sibling dist/ directory; //go:embed is constrained to the
// package directory and its descendants.
package web

import (
	"embed"
	"io/fs"
	"net/http"
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
func SPAHandler() http.Handler {
	fileSrv := http.FileServer(FS())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, err := FS().Open(r.URL.Path)
		if err != nil {
			r.URL.Path = "/"
		} else {
			_ = f.Close()
		}
		fileSrv.ServeHTTP(w, r)
	})
}
