package abs

import (
	"net/http"
	"strings"
	"time"
)

// accessLog is a minimal chi middleware that emits one Info line per
// request. Intentional during this playback-debugging window so the
// mobile client's actual traffic is visible in plugin logs without
// reaching for a packet capture. Path-only — query string is dropped
// to avoid surfacing ?token= or refresh tokens. Auth presence is
// reported as a boolean for the same reason.
//
// Demote to Debug or remove once playback issues are resolved.
func (h *Handler) accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusRecorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		auth := r.Header.Get("Authorization")
		authKind := "none"
		switch {
		case strings.HasPrefix(auth, "Bearer "):
			authKind = "bearer"
		case auth != "":
			authKind = "other"
		case r.URL.Query().Get("token") != "":
			authKind = "qtok"
		}
		// Short-circuit assets the mobile app never hits to keep the
		// signal-to-noise high while debugging.
		path := r.URL.Path
		if strings.HasPrefix(path, "/assets/") {
			return
		}
		h.logger.Info("abs req",
			"method", r.Method,
			"path", path,
			"auth", authKind,
			"status", sw.status,
			"bytes", sw.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
		)
		// Anything non-success additionally surfaces at Info so the
		// debugging window's relevant traffic isn't buried under a
		// firehose of healthy /me / /libraries calls.
		if sw.status >= 400 {
			h.logger.Warn("abs req failed",
				"method", r.Method,
				"path", path,
				"auth", authKind,
				"status", sw.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
		}
	})
}

// statusRecorder lets the access log read the status code + bytes
// written without re-implementing http.ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}
