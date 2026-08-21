package core

import (
	"testing"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

func TestPluginHostBuildsCanonicalShotAndAcceptsResult(t *testing.T) {
	sm := &StateManager{}
	sm.initialize()
	clubName := "7-iron"
	hand := protocol.RightHanded
	sm.SetClubName(&clubName)
	sm.SetHandedness(&hand)
	host := NewPluginHost(sm, nil)
	provider := host.(interface{ Timeline() *plugin.Timeline })

	receivedShots := make(chan plugin.Shot, 1)
	sub := host.OnShot(func(shot plugin.Shot) { receivedShots <- shot })
	defer sub.Close()

	sm.SetLastBallMetrics(&protocol.BallMetrics{BallSpeedMPS: 60})
	received := <-receivedShots
	if received.ID == "" || received.ClubName != "7-iron" || received.Handedness == nil {
		t.Fatalf("shot callback = %+v", received)
	}
	sm.SetLastClubMetrics(&protocol.ClubMetrics{PathAngle: 2.5})

	err := host.PublishResult(plugin.Result{
		Plugin: "hackmotion", Kind: "wrist.feedback", CorrelationID: received.ID,
	})
	if err != nil {
		t.Fatalf("PublishResult: %v", err)
	}
	shot, ok := provider.Timeline().Shot(received.ID)
	if !ok || shot.Club == nil || shot.Club.PathAngle != 2.5 || len(shot.Results) != 1 {
		t.Fatalf("canonical shot = %+v", shot)
	}
}
