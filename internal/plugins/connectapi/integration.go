package connectapi

import (
	"context"
	"encoding/json"
	"log"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/core/simulator"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

// Config describes a launch-monitor target that speaks the GSPro Connect API.
// GSPro defined the protocol; InfiniteTees and others copied it, so a single
// config-driven plugin reaches all of them. Supporting another compatible system
// is a new Config, not a new package.
type Config struct {
	Name               string   // registry/status key (e.g. "gspro", "infinitetees")
	DisplayName        string   // human-readable label for logs
	DefaultPortValue   int      // protocol default port
	ReadyMessages      []string // server messages meaning "send a shot now"
	PlayerInfoMessages []string // server messages carrying club/handedness
	ActivateOnConnect  bool     // arm ball detection immediately on connect
}

// OpenAPI returns a generic config for any system speaking the GSPro Connect
// API (GSPro, InfiniteTees, and compatible sims). It follows GSPro's behavior as
// the source of truth — ball detection arms when the server reports ready, not
// on connect — while still accepting InfiniteTees' message aliases so a single
// connection reaches every compatible sim. The "gspro" name keeps the existing
// status/settings/route plumbing while the UI presents it as "Open API".
func OpenAPI() Config {
	return Config{
		Name:               "gspro",
		DisplayName:        "Open API",
		DefaultPortValue:   921,
		ReadyMessages:      []string{"GSPro ready", "IT ready"},
		PlayerInfoMessages: []string{"GSPro Player Information", "IT Player Information"},
		ActivateOnConnect:  false,
	}
}

// Integration is the GSPro Connect API sim plugin. One implementation serves
// every compatible system, differing only by Config. It depends on the plugin
// host contract, the pure protocol types, and the core-free simulator transport.
type Integration struct {
	*simulator.Base
	cfg            Config
	host           plugin.Host
	addr           string
	port           int
	autoConnect    bool
	shotNumber     int
	lastShotNumber int
	lastStatus     plugin.Status
	lastError      error
}

// Describe advertises the plugin to the data-driven UI.
func (g *Integration) Describe() plugin.Manifest {
	return plugin.Manifest{
		Name:        g.cfg.Name,
		DisplayName: g.cfg.DisplayName,
		Icon:        "computer",
		ConfigSchema: []plugin.ConfigField{
			{Key: "host", Label: "Server IP Address", Type: plugin.FieldText},
			{Key: "port", Label: "Server Port", Type: plugin.FieldNumber, Help: "GSPro uses 921, Infinite Tees uses 999"},
			{Key: "autoConnect", Label: "Connect automatically", Type: plugin.FieldToggle},
		},
	}
}

// Config returns the current connection settings.
func (g *Integration) Config() map[string]any {
	return map[string]any{
		"host":        g.addr,
		"port":        g.port,
		"autoConnect": g.autoConnect,
	}
}

// Configure applies new connection settings.
func (g *Integration) Configure(values map[string]any) {
	if v, ok := values["host"].(string); ok {
		g.addr = v
	}
	if port, ok := toInt(values["port"]); ok {
		g.port = port
	}
	if v, ok := values["autoConnect"].(bool); ok {
		g.autoConnect = v
	}
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	default:
		return 0, false
	}
}

// New creates a Connect-API plugin for the given config and endpoint.
func New(cfg Config, addr string, port int) *Integration {
	return &Integration{
		cfg:  cfg,
		addr: addr,
		port: port,
	}
}

func (g *Integration) Name() string     { return g.cfg.Name }
func (g *Integration) DefaultPort() int { return g.cfg.DefaultPortValue }

// Start builds the TCP transport and subscribes to the device events the sim
// forwards.
func (g *Integration) Start(ctx context.Context, host plugin.Host) error {
	g.host = host
	g.Base = simulator.NewBase(g, g.addr, g.port)
	host.OnBallReady(g.onBallReadyChanged)
	host.OnBallMetrics(g.onLastBallMetricsChanged)
	host.OnClubMetrics(g.onLastClubMetricsChanged)
	return nil
}

func (g *Integration) Stop() error {
	if g.Base != nil {
		g.Base.Stop()
	}
	return nil
}

// BeginConnect owns the full connect sequence (the Connectable capability) so
// callers never orchestrate the reconnect machinery themselves.
func (g *Integration) BeginConnect(host string, port int) {
	g.Base.ResetReconnectionState()
	g.Base.EnableAutoReconnect()
	g.Base.Start()
	g.Base.Connect(host, port)
}

func (g *Integration) EndConnect() {
	g.Base.DisableAutoReconnect()
	g.Base.Disconnect()
}

func (g *Integration) report() {
	if g.host != nil {
		g.host.ReportStatus(g.cfg.Name, g.lastStatus, g.lastError)
	}
}

func (g *Integration) SetStatus(status simulator.ConnectionStatus) {
	g.lastStatus = toPluginStatus(status)
	g.report()
}

func (g *Integration) SetError(err error) {
	g.lastError = err
	g.report()
}

func toPluginStatus(status simulator.ConnectionStatus) plugin.Status {
	switch status {
	case simulator.StatusConnecting:
		return plugin.StatusConnecting
	case simulator.StatusConnected:
		return plugin.StatusConnected
	case simulator.StatusError:
		return plugin.StatusError
	default:
		return plugin.StatusDisconnected
	}
}

func (g *Integration) OnConnected() {
	g.shotNumber = 0
	g.lastShotNumber = 0
	if g.cfg.ActivateOnConnect {
		log.Printf("[%s] Connected - activating ball detection immediately", g.cfg.DisplayName)
		if err := g.host.ActivateBallDetection(); err != nil {
			log.Printf("[%s] Failed to activate ball detection: %v", g.cfg.DisplayName, err)
		}
	}
}

func (g *Integration) OnDisconnected() {
}

func (g *Integration) ProcessMessage(rawMessage string) {
	var baseMsg Message
	if err := json.Unmarshal([]byte(rawMessage), &baseMsg); err != nil {
		log.Printf("[%s] Invalid JSON: %v", g.cfg.DisplayName, err)
		return
	}

	switch {
	case contains(g.cfg.ReadyMessages, baseMsg.Message):
		g.handleReady()
	case contains(g.cfg.PlayerInfoMessages, baseMsg.Message):
		var playerInfo PlayerInfo
		if err := json.Unmarshal([]byte(rawMessage), &playerInfo); err != nil {
			log.Printf("[%s] Error parsing player info: %v", g.cfg.DisplayName, err)
			return
		}
		g.handlePlayerMessage(&playerInfo)
		g.handleReady()
	case baseMsg.Message == "Ball Data received",
		baseMsg.Message == "Club & Ball Data received",
		baseMsg.Message == "Shot received successfully":
		log.Printf("[%s] Shot data confirmed by server", g.cfg.DisplayName)
	default:
		log.Printf("[%s] Unknown message type: %s (full message: %s)", g.cfg.DisplayName, baseMsg.Message, rawMessage)
	}
}

func (g *Integration) handleReady() {
	if err := g.host.ActivateBallDetection(); err != nil {
		log.Printf("[%s] Failed to activate ball detection: %v", g.cfg.DisplayName, err)
	}
}

func (g *Integration) handlePlayerMessage(playerInfo *PlayerInfo) {
	if clubName := playerInfo.Player.Club; clubName != "" {
		clubType := g.mapClubToInternal(clubName)
		if clubType != nil {
			log.Printf("[%s] Selected club: %s (mapped to %v)", g.cfg.DisplayName, clubName, clubType)
			g.host.SetClub(clubType)
		} else {
			log.Printf("[%s] Unmapped club: %s", g.cfg.DisplayName, clubName)
		}

		g.host.SetClubName(mapClubToFriendlyName(clubName))
	}

	if handed := playerInfo.Player.Handed; handed != "" {
		handedness := protocol.RightHanded
		if handed == "LH" {
			handedness = protocol.LeftHanded
			log.Printf("[%s] Selected handedness: Left-handed", g.cfg.DisplayName)
		} else {
			log.Printf("[%s] Selected handedness: Right-handed", g.cfg.DisplayName)
		}
		g.host.SetHandedness(handedness)
	}
}

func (g *Integration) sendData(shotData ShotData) error {
	jsonData, err := json.Marshal(shotData)
	if err != nil {
		return err
	}
	return g.Base.SendMessage(jsonData)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
