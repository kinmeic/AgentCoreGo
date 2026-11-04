package server

import (
	"net/http"
	"strings"
)

func (s *Server) featureMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		disabled := (strings.HasPrefix(path, "/v1/memories") && (!s.cfg.Features.Memory || !s.cfg.Memory.Use)) ||
			(strings.HasPrefix(path, "/v1/skills") && (!s.cfg.Features.Skill || !s.cfg.Skills.Enabled)) ||
			(strings.HasPrefix(path, "/v1/mcp") && !s.cfg.Features.MCP) ||
			(strings.HasPrefix(path, "/v1/snapshots") && !s.cfg.Features.Rewind) ||
			(strings.HasPrefix(path, "/v1/agents") && !s.cfg.Features.Subagent)
		if disabled {
			respondError(w, http.StatusServiceUnavailable, "capability disabled by configuration")
			return
		}
		next.ServeHTTP(w, r)
	})
}
