package core

import (
	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

// pluginHost implements plugin.Host over the engine's StateManager and
// LaunchMonitor. It is the one place that bridges the narrow plugin contract to
// the concrete engine; plugins never see StateManager or LaunchMonitor.
type pluginHost struct {
	sm *StateManager
	lm *LaunchMonitor
}

// NewPluginHost builds the Host the composition root hands to plugins.
func NewPluginHost(sm *StateManager, lm *LaunchMonitor) plugin.Host {
	return &pluginHost{sm: sm, lm: lm}
}

func (h *pluginHost) OnBallReady(fn func(oldValue, newValue bool)) {
	h.sm.RegisterBallReadyCallback(fn)
}

func (h *pluginHost) OnBallMetrics(fn func(oldValue, newValue *protocol.BallMetrics)) {
	h.sm.RegisterLastBallMetricsCallback(fn)
}

func (h *pluginHost) OnClubMetrics(fn func(oldValue, newValue *protocol.ClubMetrics)) {
	h.sm.RegisterLastClubMetricsCallback(fn)
}

func (h *pluginHost) ActivateBallDetection() error {
	return h.lm.ActivateBallDetection()
}

func (h *pluginHost) SetClub(club *protocol.ClubType) {
	h.sm.SetClub(club)
}

func (h *pluginHost) SetClubName(name string) {
	h.sm.SetClubName(&name)
}

func (h *pluginHost) SetHandedness(handedness protocol.HandednessType) {
	h.sm.SetHandedness(&handedness)
}

func (h *pluginHost) ClubName() string {
	if name := h.sm.GetClubName(); name != nil {
		return *name
	}
	return ""
}

// statusString maps a plugin status to the generic wire string.
func statusString(status plugin.Status) string {
	switch status {
	case plugin.StatusConnecting:
		return "connecting"
	case plugin.StatusConnected:
		return "connected"
	case plugin.StatusError:
		return "error"
	default:
		return "disconnected"
	}
}

// ReportStatus records the plugin's state generically, keyed by name, which
// drives the data-driven integrations UI.
func (h *pluginHost) ReportStatus(name string, status plugin.Status, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	h.sm.SetIntegrationStatus(name, IntegrationStatus{Status: statusString(status), Error: errStr})
}
