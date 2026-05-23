package abs

import (
	"net/http"
	"strings"
	"time"
)

// accessLog is a minimal chi middleware that emits one structured line
// per request. The 2xx/3xx path logs at Debug so an Info-default plugin
// runtime stays quiet during normal playback; non-2xx escalates to Warn
// so failures still surface without an explicit log-level flip. Path is
// captured query-less so ?token= and refresh tokens never land in logs.
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
		// signal-to-noise high.
		path := r.URL.Path
		if strings.HasPrefix(path, "/assets/") {
			return
		}
		if sw.status >= 400 {
			h.logger.Warn("abs req failed",
				"method", r.Method,
				"path", path,
				"auth", authKind,
				"status", sw.status,
				"dur_ms", time.Since(start).Milliseconds(),
			)
			return
		}
		h.logger.Debug("abs req",
			"method", r.Method,
			"path", path,
			"auth", authKind,
			"status", sw.status,
			"bytes", sw.bytes,
			"dur_ms", time.Since(start).Milliseconds(),
		)
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
