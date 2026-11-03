package server

import "net/http"

func (s *Server) handleConsolidateMemories(w http.ResponseWriter, r *http.Request) {
	merged, err := s.memService.Consolidate(r.Context())
	if err != nil {
		respondError(w, http.StatusInternalServerError, "memory consolidation failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]int{"merged": merged})
}

func (s *Server) handleRebuildMemories(w http.ResponseWriter, r *http.Request) {
	if err := s.memService.Rebuild(r.Context()); err != nil {
		respondError(w, http.StatusInternalServerError, "memory index rebuild failed")
		return
	}
	respondJSON(w, http.StatusOK, map[string]string{"status": "rebuilt"})
}
