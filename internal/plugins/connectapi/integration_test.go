package connectapi

import (
	"context"
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

// fakeHost records the host calls the plugin makes so message handling and the
// arm-on-ready behavior can be asserted without the engine.
type fakeHost struct {
	activateCount int
	lastClub      *protocol.ClubType
	lastClubName  string
	lastHand      protocol.HandednessType
	handSet       bool
}

func (h *fakeHost) OnBallReady(func(bool, bool))                                     {}
func (h *fakeHost) OnBallMetrics(func(*protocol.BallMetrics, *protocol.BallMetrics)) {}
func (h *fakeHost) OnClubMetrics(func(*protocol.ClubMetrics, *protocol.ClubMetrics)) {}
func (h *fakeHost) ActivateBallDetection() error                                     { h.activateCount++; return nil }
func (h *fakeHost) SetClub(c *protocol.ClubType)                                     { h.lastClub = c }
func (h *fakeHost) SetClubName(n string)                                             { h.lastClubName = n }
func (h *fakeHost) SetHandedness(hd protocol.HandednessType)                         { h.lastHand = hd; h.handSet = true }
func (h *fakeHost) ClubName() string                                                 { return "" }
func (h *fakeHost) ReportStatus(string, plugin.Status, error)                        {}

func started(t *testing.T, cfg Config) (*Integration, *fakeHost) {
	t.Helper()
	g := New(cfg, "127.0.0.1", cfg.DefaultPortValue)
	host := &fakeHost{}
	if err := g.Start(context.Background(), host); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return g, host
}

func TestProcessMessageReadyAliasesArm(t *testing.T) {
	for _, msg := range []string{`{"Message":"GSPro ready"}`, `{"Message":"IT ready"}`} {
		g, host := started(t, OpenAPI())
		g.ProcessMessage(msg)
		if host.activateCount != 1 {
			t.Errorf("%s: activateCount = %d, want 1", msg, host.activateCount)
		}
	}
}

func TestProcessMessagePlayerInfoAliases(t *testing.T) {
	for _, msg := range []string{
		`{"Message":"GSPro Player Information","Player":{"Club":"DR","Handed":"LH"}}`,
		`{"Message":"IT Player Information","Player":{"Club":"DR","Handed":"LH"}}`,
	} {
		g, host := started(t, OpenAPI())
		g.ProcessMessage(msg)
		if host.lastClub == nil || *host.lastClub != protocol.ClubDriver {
			t.Errorf("%s: club not set to Driver", msg)
		}
		if host.lastClubName != "DR" {
			t.Errorf("%s: club name = %q, want DR", msg, host.lastClubName)
		}
		if !host.handSet || host.lastHand != protocol.LeftHanded {
			t.Errorf("%s: handedness not set to Left", msg)
		}
		// Player info also triggers the ready/arm path.
		if host.activateCount != 1 {
			t.Errorf("%s: activateCount = %d, want 1", msg, host.activateCount)
		}
	}
}

func TestOpenAPIDoesNotArmOnConnect(t *testing.T) {
	g, host := started(t, OpenAPI())
	g.OnConnected()
	if host.activateCount != 0 {
		t.Errorf("OpenAPI OnConnected armed ball detection (count=%d); GSPro behavior is arm-on-ready only", host.activateCount)
	}
}

func TestActivateOnConnectConfigArms(t *testing.T) {
	cfg := OpenAPI()
	cfg.ActivateOnConnect = true
	g, host := started(t, cfg)
	g.OnConnected()
	if host.activateCount != 1 {
		t.Errorf("ActivateOnConnect:true OnConnected count = %d, want 1", host.activateCount)
	}
}

func TestConfigRoundTripWithFloatPort(t *testing.T) {
	g := New(OpenAPI(), "127.0.0.1", 921)
	// Simulate a JSON reload where numbers arrive as float64.
	g.Configure(map[string]any{"host": "1.2.3.4", "port": float64(999), "autoConnect": true})
	cfg := g.Config()
	if cfg["host"] != "1.2.3.4" {
		t.Errorf("host = %v, want 1.2.3.4", cfg["host"])
	}
	if cfg["port"] != 999 {
		t.Errorf("port = %v (%T), want int 999", cfg["port"], cfg["port"])
	}
	if cfg["autoConnect"] != true {
		t.Errorf("autoConnect = %v, want true", cfg["autoConnect"])
	}
}
