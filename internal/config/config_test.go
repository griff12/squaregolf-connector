package config

import (
	"os"
	"testing"
)

// TestMain redirects HOME to a temp dir before the config singleton initializes,
// so Save() does not touch the real ~/.squaregolf-connector.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sgc-config-test")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestIntegrationConfigAbsentReturnsNil(t *testing.T) {
	m := GetInstance()
	if got := m.GetIntegrationConfig("never-set"); got != nil {
		t.Errorf("GetIntegrationConfig(absent) = %v, want nil (so seed falls back to typed settings)", got)
	}
}

func TestIntegrationConfigRoundTripPersists(t *testing.T) {
	m := GetInstance()
	want := map[string]any{"host": "10.0.0.5", "port": float64(921), "autoConnect": true}
	if err := m.SetIntegrationConfig("gspro", want); err != nil {
		t.Fatalf("SetIntegrationConfig: %v", err)
	}

	// In-memory read.
	if got := m.GetIntegrationConfig("gspro"); got["host"] != "10.0.0.5" {
		t.Errorf("in-memory host = %v, want 10.0.0.5", got["host"])
	}

	// Survives a reload from disk (the seed path on next boot).
	m.settings.Integrations = nil
	if err := m.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := m.GetIntegrationConfig("gspro")
	if got == nil || got["host"] != "10.0.0.5" || got["autoConnect"] != true {
		t.Errorf("after reload, config = %v, want host=10.0.0.5 autoConnect=true", got)
	}
}
