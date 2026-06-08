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

// ReportStatus records the plugin's state generically (keyed by name, for the
// data-driven UI) and also into the legacy per-integration fields, preserving
// the existing WebSocket contract until the hardcoded API is removed.
func (h *pluginHost) ReportStatus(name string, status plugin.Status, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	h.sm.SetIntegrationStatus(name, IntegrationStatus{Status: statusString(status), Error: errStr})

	switch name {
	case "gspro":
		h.sm.SetGSProStatus(toGSProStatus(status))
		h.sm.SetGSProError(err)
	case "infinitetees":
		h.sm.SetInfiniteTeesStatus(toInfiniteTeesStatus(status))
		h.sm.SetInfiniteTeesError(err)
	case "camera":
		h.sm.SetCameraStatus(toCameraStatus(status))
		h.sm.SetCameraError(err)
	}
}

func toGSProStatus(status plugin.Status) GSProConnectionStatus {
	switch status {
	case plugin.StatusConnecting:
		return GSProStatusConnecting
	case plugin.StatusConnected:
		return GSProStatusConnected
	case plugin.StatusError:
		return GSProStatusError
	default:
		return GSProStatusDisconnected
	}
}

func toInfiniteTeesStatus(status plugin.Status) InfiniteTeesConnectionStatus {
	switch status {
	case plugin.StatusConnecting:
		return InfiniteTeesStatusConnecting
	case plugin.StatusConnected:
		return InfiniteTeesStatusConnected
	case plugin.StatusError:
		return InfiniteTeesStatusError
	default:
		return InfiniteTeesStatusDisconnected
	}
}

func toCameraStatus(status plugin.Status) CameraConnectionStatus {
	switch status {
	case plugin.StatusError:
		return CameraStatusError
	case plugin.StatusConnected, plugin.StatusConnecting:
		return CameraStatusOK
	default:
		return CameraStatusUnknown
	}
}
