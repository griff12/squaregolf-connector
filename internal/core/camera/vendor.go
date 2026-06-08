package camera

import "github.com/brentyates/squaregolf-connector/internal/core"

// Vendor abstracts a specific external camera system's API. The Manager owns the
// vendor-neutral orchestration (enable/disable, shot buffering, status
// reporting) and delegates the actual device calls to a Vendor.
//
// This is the open/closed seam for cameras: adding support for a new camera
// system is a new file implementing Vendor plus one line selecting it in the
// composition root — no changes to Manager or the state listeners.
type Vendor interface {
	// Name identifies the vendor (for logging/diagnostics).
	Name() string
	// SetBaseURL updates the endpoint the vendor talks to.
	SetBaseURL(baseURL string)
	// Arm tells the camera to start a new recording.
	Arm() error
	// ShotDetected stops recording and saves the clip with ball metrics,
	// returning the saved clip's filename (may be empty if the vendor does not
	// report one).
	ShotDetected(ball *core.BallMetrics) (filename string, err error)
	// UpdateMetadata attaches club metrics (and the resolved club name) to a
	// previously saved clip.
	UpdateMetadata(filename string, club *core.ClubMetrics, clubName string) error
	// Cancel aborts an in-progress recording.
	Cancel() error
}
