package web

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/gorilla/mux"
)

// integrationView is one integration's full state for the data-driven UI: its
// self-described manifest and capabilities, its current config, and live status.
type integrationView struct {
	plugin.View
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

// view builds the full view for one plugin, or false if it does not exist /
// does not describe itself.
func (s *Server) view(name string) (integrationView, bool) {
	for _, v := range s.plugins.Views() {
		if v.Name == name {
			st := s.stateManager.GetIntegrationStatus(name)
			return integrationView{View: v, Status: st.Status, Error: st.Error}, true
		}
	}
	return integrationView{}, false
}

// handleIntegrations lists every registered integration with manifest, config,
// and status. The frontend renders the UI entirely from this — adding a plugin
// needs no frontend changes.
func (s *Server) handleIntegrations(w http.ResponseWriter, r *http.Request) {
	views := s.plugins.Views()
	out := make([]integrationView, 0, len(views))
	for _, v := range views {
		st := s.stateManager.GetIntegrationStatus(v.Name)
		out = append(out, integrationView{View: v, Status: st.Status, Error: st.Error})
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(out)
}

func (s *Server) handleIntegrationConnect(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if lifecycle, ok := s.plugins.ConnectionLifecycle(name); ok {
		go func() {
			if err := lifecycle.Connect(context.Background()); err != nil {
				log.Printf("integration %q connect failed: %v", name, err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	c, ok := s.plugins.Connectable(name)
	if !ok {
		http.Error(w, "integration is not connectable", http.StatusNotFound)
		return
	}

	host, port := "127.0.0.1", 0
	if cs, ok := s.plugins.ConfigStore(name); ok {
		cfg := cs.Config()
		if v, ok := cfg["host"].(string); ok {
			host = v
		}
		if v, ok := cfg["port"].(int); ok {
			port = v
		}
	}

	go c.BeginConnect(host, port)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleIntegrationDisconnect(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	if lifecycle, ok := s.plugins.ConnectionLifecycle(name); ok {
		go func() {
			if err := lifecycle.Disconnect(context.Background()); err != nil {
				log.Printf("integration %q disconnect failed: %v", name, err)
			}
		}()
		w.WriteHeader(http.StatusAccepted)
		return
	}
	c, ok := s.plugins.Connectable(name)
	if !ok {
		http.Error(w, "integration is not connectable", http.StatusNotFound)
		return
	}
	go c.EndConnect()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleIntegrationConfig(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	cs, ok := s.plugins.ConfigStore(name)
	if !ok {
		http.Error(w, "integration is not configurable", http.StatusNotFound)
		return
	}

	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cs.Config())
		return
	}

	var values map[string]any
	if err := json.NewDecoder(r.Body).Decode(&values); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if integration, ok := s.plugins.Get(name); ok {
		if validator, ok := integration.(plugin.ConfigValidator); ok {
			if err := validator.ValidateConfig(values); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
	}
	cs.Configure(values)
	if err := config.GetInstance().SetIntegrationConfig(name, cs.Config()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.broadcastIntegration(name)
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleIntegrationAction(w http.ResponseWriter, r *http.Request) {
	name := mux.Vars(r)["name"]
	actionName := mux.Vars(r)["action"]
	actionable, ok := s.plugins.Actionable(name)
	if !ok {
		http.Error(w, "integration does not expose actions", http.StatusNotFound)
		return
	}
	input := map[string]any{}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}
	}
	result, err := actionable.Invoke(r.Context(), actionName, input)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// broadcastIntegration pushes one integration's current view over the WebSocket.
func (s *Server) broadcastIntegration(name string) {
	view, ok := s.view(name)
	if !ok {
		return
	}
	data, _ := json.Marshal(WSMessage{Type: "integrationStatus", Data: view})
	select {
	case s.broadcast <- data:
	default:
	}
}
