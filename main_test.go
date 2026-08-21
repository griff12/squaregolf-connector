package main

import (
	"os"
	"testing"

	appcfg "github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/brentyates/squaregolf-connector/internal/plugins/connectapi"
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sgc-main-test")
	if err != nil {
		panic(err)
	}
	if err := os.Setenv("HOME", dir); err != nil {
		panic(err)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func TestSeedIntegrationConfigUsesLegacySettingsWhenGenericConfigIsAbsent(t *testing.T) {
	settings := appcfg.GetInstance().GetSettings()
	settings.GSProIP = "10.0.0.42"
	settings.GSProPort = 999
	settings.GSProAutoConnect = true
	if err := appcfg.GetInstance().UpdateSettings(settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}

	registry := plugin.NewRegistry(core.NewPluginHost(nil, nil))
	registry.Register(connectapi.New(connectapi.OpenAPI(), "127.0.0.1", 921))
	seedIntegrationConfig(registry, "gspro", legacyGSProConfig(AppConfig{}, settings))

	store, ok := registry.ConfigStore("gspro")
	if !ok {
		t.Fatal("gspro plugin is not configurable")
	}
	got := store.Config()
	if got["host"] != "10.0.0.42" || got["port"] != 999 || got["autoConnect"] != true {
		t.Fatalf("seeded config = %v, want saved legacy endpoint and auto-connect", got)
	}
}

func TestLegacyGSProConfigUsesExplicitCLIEndpointWhenEnabled(t *testing.T) {
	settings := appcfg.Settings{GSProIP: "10.0.0.42", GSProPort: 999, GSProAutoConnect: true}
	config := AppConfig{EnableGSPro: true, GSProIP: "192.168.1.8", GSProPort: 921}

	got := legacyGSProConfig(config, settings)
	if got["host"] != "192.168.1.8" || got["port"] != 921 || got["autoConnect"] != true {
		t.Fatalf("legacy config = %v, want explicit CLI endpoint and saved auto-connect", got)
	}
}
