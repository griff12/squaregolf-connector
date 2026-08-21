package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/gorilla/mux"
)

func TestShotEndpointsReturnCanonicalHistory(t *testing.T) {
	registry := plugin.NewRegistry(nil)
	shot := registry.Timeline().RecordShot(&protocol.BallMetrics{BallSpeedMPS: 61}, nil, "Driver", nil)
	if _, err := registry.Timeline().PublishResult(plugin.Result{
		Plugin: "hackmotion", Kind: "wrist.feedback", CorrelationID: shot.ID,
		Summary: "Impact wrist was in range",
	}); err != nil {
		t.Fatalf("PublishResult: %v", err)
	}

	server := &Server{plugins: registry}
	router := mux.NewRouter()
	router.HandleFunc("/api/shots", server.handleShots).Methods(http.MethodGet)
	router.HandleFunc("/api/shots/{id}", server.handleShot).Methods(http.MethodGet)

	list := httptest.NewRecorder()
	router.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/api/shots?limit=10", nil))
	if list.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", list.Code, list.Body.String())
	}
	var shots []plugin.Shot
	if err := json.Unmarshal(list.Body.Bytes(), &shots); err != nil {
		t.Fatalf("decode shots: %v", err)
	}
	if len(shots) != 1 || shots[0].ID != shot.ID || len(shots[0].Results) != 1 {
		t.Fatalf("shots = %+v", shots)
	}

	detail := httptest.NewRecorder()
	router.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/api/shots/"+shot.ID, nil))
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d, body = %s", detail.Code, detail.Body.String())
	}
}

func TestShotEndpointValidatesLimit(t *testing.T) {
	server := &Server{plugins: plugin.NewRegistry(nil)}
	recorder := httptest.NewRecorder()
	server.handleShots(recorder, httptest.NewRequest(http.MethodGet, "/api/shots?limit=0", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}

func TestShotTimelineBroadcastsPluginResult(t *testing.T) {
	registry := plugin.NewRegistry(nil)
	server := &Server{plugins: registry, broadcast: make(chan []byte, 3)}
	sub := registry.Timeline().Subscribe(server.broadcastShotEvent)
	defer sub.Close()

	shot := registry.Timeline().RecordShot(&protocol.BallMetrics{}, nil, "", nil)
	<-server.broadcast // shot.created
	if _, err := registry.Timeline().PublishResult(plugin.Result{
		Plugin: "camera", Kind: "media", CorrelationID: shot.ID,
	}); err != nil {
		t.Fatalf("PublishResult: %v", err)
	}

	var message WSMessage
	if err := json.Unmarshal(<-server.broadcast, &message); err != nil {
		t.Fatalf("decode broadcast: %v", err)
	}
	if message.Type != "shotEvent" {
		t.Fatalf("message type = %q, want shotEvent", message.Type)
	}
}
