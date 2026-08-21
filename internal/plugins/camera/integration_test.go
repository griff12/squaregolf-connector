package camera

import (
	"context"
	"errors"
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

// fakeHost captures status reports and satisfies plugin.Host so the camera
// Manager can be tested without the engine.
type fakeHost struct {
	lastStatus plugin.Status
	lastErr    error
}

func (h *fakeHost) OnBallReady(func(bool, bool))                                     {}
func (h *fakeHost) OnBallMetrics(func(*protocol.BallMetrics, *protocol.BallMetrics)) {}
func (h *fakeHost) OnClubMetrics(func(*protocol.ClubMetrics, *protocol.ClubMetrics)) {}
func (h *fakeHost) OnShot(func(plugin.Shot)) plugin.Subscription                     { return nil }
func (h *fakeHost) ActivateBallDetection() error                                     { return nil }
func (h *fakeHost) SetClub(*protocol.ClubType)                                       {}
func (h *fakeHost) SetClubName(string)                                               {}
func (h *fakeHost) SetHandedness(protocol.HandednessType)                            {}
func (h *fakeHost) ClubName() string                                                 { return "" }
func (h *fakeHost) ReportStatus(_ string, status plugin.Status, err error) {
	h.lastStatus = status
	h.lastErr = err
}
func (h *fakeHost) PublishResult(plugin.Result) error { return nil }

// fakeVendor records calls so we can prove the Manager drives any Vendor without
// knowing its concrete type — the camera open/closed seam.
type fakeVendor struct {
	armCalled    bool
	cancelCalled bool
	shotBall     *protocol.BallMetrics
	shotFilename string
	shotErr      error
}

func (f *fakeVendor) Name() string      { return "fake" }
func (f *fakeVendor) SetBaseURL(string) {}
func (f *fakeVendor) Arm() error        { f.armCalled = true; return nil }
func (f *fakeVendor) Cancel() error     { f.cancelCalled = true; return nil }
func (f *fakeVendor) ShotDetected(b *protocol.BallMetrics) (string, error) {
	f.shotBall = b
	return f.shotFilename, f.shotErr
}
func (f *fakeVendor) UpdateMetadata(string, *protocol.ClubMetrics, string) error { return nil }

func startManager(t *testing.T, vendor Vendor, enabled bool) (*Manager, *fakeHost) {
	t.Helper()
	m := New(vendor, "http://localhost:5000", enabled)
	host := &fakeHost{}
	if err := m.Start(context.Background(), host); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	return m, host
}

func TestManagerDelegatesToVendor(t *testing.T) {
	fv := &fakeVendor{}
	m, _ := startManager(t, fv, true)

	if err := m.Arm(); err != nil {
		t.Fatalf("Arm() error: %v", err)
	}
	if !fv.armCalled {
		t.Error("Arm() did not reach the vendor")
	}

	if err := m.Cancel(); err != nil {
		t.Fatalf("Cancel() error: %v", err)
	}
	if !fv.cancelCalled {
		t.Error("Cancel() did not reach the vendor")
	}

	ball := &protocol.BallMetrics{BallSpeedMPS: 50}
	if err := m.ShotDetected(ball); err != nil {
		t.Fatalf("ShotDetected() error: %v", err)
	}
	if fv.shotBall != ball {
		t.Error("ShotDetected() did not pass ball metrics to the vendor")
	}
}

func TestManagerDisabledSkipsVendor(t *testing.T) {
	fv := &fakeVendor{}
	m, _ := startManager(t, fv, false)

	_ = m.Arm()
	if fv.armCalled {
		t.Error("disabled Manager should not call the vendor")
	}
}

func TestManagerRecordsVendorError(t *testing.T) {
	fv := &fakeVendor{shotErr: errors.New("boom")}
	m, host := startManager(t, fv, true)

	_ = m.ShotDetected(&protocol.BallMetrics{})
	if host.lastStatus != plugin.StatusError {
		t.Errorf("expected StatusError after vendor failure, got %v", host.lastStatus)
	}
	if host.lastErr == nil {
		t.Error("expected error to be reported to the host")
	}
}
