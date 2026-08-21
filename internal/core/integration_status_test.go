package core

import "testing"

func TestIntegrationStatusRoundTrip(t *testing.T) {
	sm := &StateManager{}
	sm.SetIntegrationStatus("gspro", IntegrationStatus{Status: "connected"})

	got := sm.GetIntegrationStatus("gspro")
	if got.Status != "connected" {
		t.Errorf("Status = %q, want connected", got.Status)
	}
}

func TestIntegrationStatusUnknownDefaultsDisconnected(t *testing.T) {
	sm := &StateManager{}
	if got := sm.GetIntegrationStatus("nope").Status; got != "disconnected" {
		t.Errorf("unknown integration Status = %q, want disconnected", got)
	}
}

func TestIntegrationStatusCallbackFires(t *testing.T) {
	sm := &StateManager{}
	var gotName string
	var gotStatus IntegrationStatus
	sm.RegisterIntegrationStatusCallback(func(name string, status IntegrationStatus) {
		gotName = name
		gotStatus = status
	})

	sm.SetIntegrationStatus("camera", IntegrationStatus{Status: "error", Error: "boom"})
	if gotName != "camera" || gotStatus.Status != "error" || gotStatus.Error != "boom" {
		t.Errorf("callback got (%q, %+v), want (camera, error/boom)", gotName, gotStatus)
	}
}

func TestIntegrationStatusCallbackPanicIsolated(t *testing.T) {
	sm := &StateManager{}
	reached := false
	sm.RegisterIntegrationStatusCallback(func(string, IntegrationStatus) { panic("boom") })
	sm.RegisterIntegrationStatusCallback(func(string, IntegrationStatus) { reached = true })

	// Must not panic, and the second callback must still run.
	sm.SetIntegrationStatus("gspro", IntegrationStatus{Status: "connected"})
	if !reached {
		t.Error("a panicking callback aborted the fan-out")
	}
}
