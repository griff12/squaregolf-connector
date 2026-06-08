// Package plugin defines the contract between the SquareGolf engine and the
// external capabilities that plug into it (sim integrations, cameras, ...).
//
// Plugins depend only on this package and the pure protocol types — never on the
// core engine. The engine provides a Host implementation and is assembled, with
// its plugins, solely in the composition root (main). Because core and web do
// not import any plugin package, a plugin can be deleted and the engine still
// compiles: the defining property of treating integrations as outsiders.
package plugin

import (
	"context"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
)

// Status is the connection/health state a plugin reports to the host.
type Status int

const (
	StatusDisconnected Status = iota
	StatusConnecting
	StatusConnected
	StatusError
)

// Host is the narrow surface the engine exposes to plugins: the device events
// they may subscribe to, the capabilities they may invoke, and a channel to
// report their own health. It is deliberately small — a plugin sees only this.
type Host interface {
	// OnBallReady subscribes to ball-ready changes (old, new).
	OnBallReady(fn func(oldValue, newValue bool))
	// OnBallMetrics subscribes to ball-metrics changes (old, new); a new shot
	// arrives as a new non-nil value.
	OnBallMetrics(fn func(oldValue, newValue *protocol.BallMetrics))
	// OnClubMetrics subscribes to club-metrics changes (old, new).
	OnClubMetrics(fn func(oldValue, newValue *protocol.ClubMetrics))

	// ActivateBallDetection asks the launch monitor to arm ball detection.
	ActivateBallDetection() error
	// SetClub sets the currently selected club.
	SetClub(club *protocol.ClubType)
	// SetClubName sets the human-readable club name (e.g. "7-iron").
	SetClubName(name string)
	// SetHandedness sets player handedness.
	SetHandedness(handedness protocol.HandednessType)
	// ClubName returns the current human-readable club name, or "".
	ClubName() string

	// ReportStatus surfaces a plugin's connection/health state into app state.
	ReportStatus(plugin string, status Status, err error)
}

// Plugin is an external capability registered against the engine. The engine
// drives it through this interface without knowing its concrete type.
type Plugin interface {
	Name() string
	Start(ctx context.Context, host Host) error
	Stop() error
}

// Connectable is an optional capability implemented by plugins that maintain a
// user-controlled network connection (the sim integrations). The web layer
// type-asserts for it to expose connect/disconnect routes; the plugin owns the
// full connect sequence (reset, enable auto-reconnect, dial) behind BeginConnect.
type Connectable interface {
	BeginConnect(host string, port int)
	EndConnect()
}
