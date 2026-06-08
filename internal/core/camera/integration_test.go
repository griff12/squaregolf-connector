package camera

import (
	"errors"
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// fakeVendor records calls so we can prove the Manager drives any Vendor without
// knowing its concrete type — the camera open/closed seam.
type fakeVendor struct {
	armCalled    bool
	cancelCalled bool
	shotBall     *core.BallMetrics
	metaFilename string
	metaClubName string
	shotFilename string
	shotErr      error
}

func (f *fakeVendor) Name() string      { return "fake" }
func (f *fakeVendor) SetBaseURL(string) {}
func (f *fakeVendor) Arm() error        { f.armCalled = true; return nil }
func (f *fakeVendor) Cancel() error     { f.cancelCalled = true; return nil }
func (f *fakeVendor) ShotDetected(b *core.BallMetrics) (string, error) {
	f.shotBall = b
	return f.shotFilename, f.shotErr
}
func (f *fakeVendor) UpdateMetadata(filename string, _ *core.ClubMetrics, clubName string) error {
	f.metaFilename = filename
	f.metaClubName = clubName
	return nil
}

func TestManagerDelegatesToVendor(t *testing.T) {
	sm := core.GetInstance()
	fv := &fakeVendor{}
	m := NewManager(sm, fv, true)

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

	ball := &core.BallMetrics{BallSpeedMPS: 50}
	if err := m.ShotDetected(ball); err != nil {
		t.Fatalf("ShotDetected() error: %v", err)
	}
	if fv.shotBall != ball {
		t.Error("ShotDetected() did not pass ball metrics to the vendor")
	}
}

func TestManagerDisabledSkipsVendor(t *testing.T) {
	sm := core.GetInstance()
	fv := &fakeVendor{}
	m := NewManager(sm, fv, false)

	_ = m.Arm()
	if fv.armCalled {
		t.Error("disabled Manager should not call the vendor")
	}
}

func TestManagerRecordsVendorError(t *testing.T) {
	sm := core.GetInstance()
	fv := &fakeVendor{shotErr: errors.New("boom")}
	m := NewManager(sm, fv, true)

	_ = m.ShotDetected(&core.BallMetrics{})
	if sm.GetCameraStatus() != core.CameraStatusError {
		t.Errorf("expected camera status error after vendor failure, got %v", sm.GetCameraStatus())
	}
	if sm.GetCameraError() == nil {
		t.Error("expected camera error to be recorded")
	}
}
