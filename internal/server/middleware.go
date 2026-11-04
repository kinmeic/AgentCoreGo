package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

type serverMetrics struct {
	requests      atomic.Int64
	errors        atomic.Int64
	requestMicros atomic.Int64
}

type rateWindow struct {
	started time.Time
	count   int
}

func (s *Server) bodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.Server.MaxRequestBytes > 0 {
			r.Body = http.MaxBytesReader(w, r.Body, s.cfg.Server.MaxRequestBytes)
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	allowed := make(map[string]bool, len(s.cfg.Server.CORSOrigins))
	for _, origin := range s.cfg.Server.CORSOrigins {
		allowed[origin] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowed[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, X-Command-ID, Last-Event-ID")
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, X-Run-ID, X-Next-Cursor")
			w.Header().Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			if origin != "" && !allowed[origin] {
				respondError(w, http.StatusForbidden, "origin is not allowed")
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rateLimitMiddleware(next http.Handler) http.Handler {
	limit := s.cfg.Server.RateLimitPerMinute
	var mu sync.Mutex
	windows := make(map[string]*rateWindow)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limit <= 0 || r.URL.Path == "/health" {
			next.ServeHTTP(w, r)
			return
		}
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		now := time.Now()
		mu.Lock()
		window := windows[host]
		if window == nil || now.Sub(window.started) >= time.Minute {
			window = &rateWindow{started: now}
			windows[host] = window
		}
		window.count++
		count := window.count
		if len(windows) > 10_000 {
			for key, candidate := range windows {
				if now.Sub(candidate.started) >= 2*time.Minute {
					delete(windows, key)
				}
			}
		}
		mu.Unlock()
		if count > limit {
			w.Header().Set("Retry-After", "60")
			respondError(w, http.StatusTooManyRequests, "rate limit exceeded")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type requestIDKey struct{}

func (s *Server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := r.Header.Get("X-Request-ID")
		if requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\r\n") {
			requestID = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", requestID)
		writer := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		started := time.Now()
		next.ServeHTTP(writer, r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID)))
		duration := time.Since(started)
		s.metrics.requests.Add(1)
		s.metrics.requestMicros.Add(duration.Microseconds())
		if writer.status >= 500 {
			s.metrics.errors.Add(1)
		}
		slog.Info("http request", "request_id", requestID, "method", r.Method, "path", r.URL.Path,
			"status", writer.status, "duration_ms", duration.Milliseconds())
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *statusWriter) Write(data []byte) (int, error) { return w.ResponseWriter.Write(data) }
func (w *statusWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (s *Server) handleReady(w http.ResponseWriter, _ *http.Request) {
	checks := map[string]string{"database": "ok", "workspace": "ok"}
	status := http.StatusOK
	if err := s.store.Ping(); err != nil {
		checks["database"] = "unavailable"
		status = http.StatusServiceUnavailable
	}
	if info, err := os.Stat(s.cfg.Workspace.Path); err != nil || !info.IsDir() {
		checks["workspace"] = "unavailable"
		status = http.StatusServiceUnavailable
	}
	if s.cfg.LLM.BaseURL == "" || s.cfg.LLM.Model == "" {
		checks["llm"] = "not_configured"
		status = http.StatusServiceUnavailable
	} else {
		checks["llm"] = "configured"
	}
	respondJSON(w, status, map[string]any{"status": map[bool]string{true: "ready", false: "not_ready"}[status == http.StatusOK], "checks": checks})
}

func (s *Server) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	requests := s.metrics.requests.Load()
	var average float64
	if requests > 0 {
		average = float64(s.metrics.requestMicros.Load()) / float64(requests) / 1000
	}
	respondJSON(w, http.StatusOK, map[string]any{"requests_total": requests,
		"errors_total": s.metrics.errors.Load(), "average_request_ms": average,
		"tools": s.toolRegistry.Count(), "mcp_connections": len(s.mcpManager.ListServers())})
}
