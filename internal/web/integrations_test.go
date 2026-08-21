package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/gorilla/mux"
)

// TestMain redirects the config dir to a temp HOME before the config singleton
// initializes, so POST /config does not pollute the real ~/.squaregolf-connector.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sgc-web-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// fakeIntegration implements the plugin contract the generic handlers exercise.
type fakeIntegration struct {
	name string
	cfg  map[string]any
}

func (f *fakeIntegration) Name() string                             { return f.name }
func (f *fakeIntegration) Start(context.Context, plugin.Host) error { return nil }
func (f *fakeIntegration) Stop() error                              { return nil }
func (f *fakeIntegration) Describe() plugin.Manifest {
	return plugin.Manifest{
		Name:        f.name,
		DisplayName: "Fake",
		Icon:        "extension",
		ConfigSchema: []plugin.ConfigField{
			{Key: "host", Label: "Host", Type: plugin.FieldText},
		},
	}
}
func (f *fakeIntegration) Config() map[string]any { return f.cfg }
func (f *fakeIntegration) Configure(v map[string]any) {
	for k, val := range v {
		f.cfg[k] = val
	}
}
func (f *fakeIntegration) ValidateConfig(values map[string]any) error {
	if values["host"] == "invalid" {
		return errors.New("invalid host")
	}
	return nil
}
func (f *fakeIntegration) Invoke(_ context.Context, action string, _ map[string]any) (map[string]any, error) {
	if action != "scan" {
		return nil, errors.New("unknown action")
	}
	return map[string]any{"devices": []any{"sensor-1"}}, nil
}

func newTestServer(plugins *plugin.Registry) (*Server, *mux.Router) {
	s := &Server{
		stateManager: core.GetInstance(),
		plugins:      plugins,
		broadcast:    make(chan []byte, 10),
	}
	r := mux.NewRouter()
	api := r.PathPrefix("/api").Subrouter()
	api.HandleFunc("/integrations", s.handleIntegrations).Methods("GET")
	api.HandleFunc("/integrations/{name}/config", s.handleIntegrationConfig).Methods("GET", "POST")
	api.HandleFunc("/integrations/{name}/actions/{action}", s.handleIntegrationAction).Methods("POST")
	return s, r
}

func TestHandleIntegrationConfigRejectsInvalidValues(t *testing.T) {
	fake := &fakeIntegration{name: "fake", cfg: map[string]any{"host": "old"}}
	reg := plugin.NewRegistry(nil)
	reg.Register(fake)
	_, r := newTestServer(reg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/fake/config", strings.NewReader(`{"host":"invalid"}`))
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || fake.cfg["host"] != "old" {
		t.Fatalf("status = %d, config = %v", rec.Code, fake.cfg)
	}
}

func TestHandleIntegrationAction(t *testing.T) {
	fake := &fakeIntegration{name: "fake", cfg: map[string]any{}}
	reg := plugin.NewRegistry(nil)
	reg.Register(fake)
	_, r := newTestServer(reg)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/integrations/fake/actions/scan", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "sensor-1") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleIntegrationsShape(t *testing.T) {
	reg := plugin.NewRegistry(nil)
	reg.Register(&fakeIntegration{name: "fake", cfg: map[string]any{"host": "1.1.1.1"}})
	_, r := newTestServer(reg)

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/integrations", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var views []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &views); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(views) != 1 {
		t.Fatalf("got %d integrations, want 1", len(views))
	}
	v := views[0]
	for _, key := range []string{"name", "displayName", "configSchema", "config", "status"} {
		if _, ok := v[key]; !ok {
			t.Errorf("integration view missing %q (have %v)", key, v)
		}
	}
	if v["name"] != "fake" {
		t.Errorf("name = %v, want fake", v["name"])
	}
}

func TestHandleIntegrationConfigPersistsAndBroadcasts(t *testing.T) {
	fake := &fakeIntegration{name: "fake", cfg: map[string]any{"host": "old"}}
	reg := plugin.NewRegistry(nil)
	reg.Register(fake)
	s, r := newTestServer(reg)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/fake/config", strings.NewReader(`{"host":"new"}`))
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if fake.cfg["host"] != "new" {
		t.Errorf("Configure not applied: host = %v", fake.cfg["host"])
	}
	if persisted := config.GetInstance().GetIntegrationConfig("fake"); persisted["host"] != "new" {
		t.Errorf("config not persisted: %v", persisted)
	}
	select {
	case data := <-s.broadcast:
		var msg WSMessage
		json.Unmarshal(data, &msg)
		if msg.Type != "integrationStatus" {
			t.Errorf("broadcast type = %q, want integrationStatus", msg.Type)
		}
	default:
		t.Error("config change did not broadcast an integrationStatus message")
	}
}
