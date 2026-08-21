package web

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/gorilla/mux"
)

func (s *Server) handleShots(w http.ResponseWriter, r *http.Request) {
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.plugins.Timeline().Shots(limit))
}

func (s *Server) handleShot(w http.ResponseWriter, r *http.Request) {
	shot, ok := s.plugins.Timeline().Shot(mux.Vars(r)["id"])
	if !ok {
		http.Error(w, "shot not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(shot)
}

func (s *Server) broadcastShotEvent(event plugin.ShotEvent) {
	data, err := json.Marshal(WSMessage{Type: "shotEvent", Data: event})
	if err != nil {
		return
	}
	select {
	case s.broadcast <- data:
	default:
	}
}
