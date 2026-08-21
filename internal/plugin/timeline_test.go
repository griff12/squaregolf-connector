package plugin

import (
	"testing"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
)

func TestTimelineCorrelatesClubAndPluginResult(t *testing.T) {
	timeline := NewTimeline()
	timeline.now = func() time.Time { return time.Unix(100, 0) }

	events := make(chan ShotEventKind, 3)
	sub := timeline.Subscribe(func(event ShotEvent) { events <- event.Kind })
	defer sub.Close()

	ball := &protocol.BallMetrics{BallSpeedMPS: 62.5}
	shot := timeline.RecordShot(ball, &protocol.ClubIron7, "7-iron", nil)
	if shot.ID == "" || shot.Sequence != 1 || shot.Ball.BallSpeedMPS != 62.5 {
		t.Fatalf("recorded shot = %+v", shot)
	}

	club := &protocol.ClubMetrics{ClubSpeed: 45.2, IsClubSpeedValid: true}
	if _, ok := timeline.UpdateLatestClub(club); !ok {
		t.Fatal("UpdateLatestClub did not find the shot")
	}

	result := Result{
		Plugin: "hackmotion", Kind: "wrist.feedback", CorrelationID: shot.ID,
		Summary: "Wrist extended through impact",
		Metrics: []Metric{{Key: "flex-impact", Label: "Impact flexion", Value: 18.2, Unit: "deg"}},
	}
	updated, err := timeline.PublishResult(result)
	if err != nil {
		t.Fatalf("PublishResult: %v", err)
	}
	if updated.Club == nil || updated.Club.ClubSpeed != 45.2 || len(updated.Results) != 1 {
		t.Fatalf("updated shot = %+v", updated)
	}
	wantEvents := []ShotEventKind{ShotCreated, ShotUpdated, ShotResultAdded}
	for _, want := range wantEvents {
		if got := <-events; got != want {
			t.Fatalf("event = %v, want %v", got, want)
		}
	}
}

func TestTimelineSnapshotsDoNotExposeMutableState(t *testing.T) {
	timeline := NewTimeline()
	ball := &protocol.BallMetrics{RawData: []string{"11", "02"}, BallSpeedMPS: 50}
	shot := timeline.RecordShot(ball, nil, "", nil)
	ball.BallSpeedMPS = 1
	ball.RawData[0] = "changed"

	snapshot, ok := timeline.Shot(shot.ID)
	if !ok {
		t.Fatal("shot not found")
	}
	if snapshot.Ball.BallSpeedMPS != 50 || snapshot.Ball.RawData[0] != "11" {
		t.Fatalf("timeline retained caller mutation: %+v", snapshot.Ball)
	}

	snapshot.Ball.BallSpeedMPS = 2
	again, _ := timeline.Shot(shot.ID)
	if again.Ball.BallSpeedMPS != 50 {
		t.Fatalf("snapshot mutated timeline: %+v", again.Ball)
	}
}

func TestTimelineRejectsUncorrelatedResult(t *testing.T) {
	timeline := NewTimeline()
	_, err := timeline.PublishResult(Result{Plugin: "camera", Kind: "media", CorrelationID: "missing"})
	if err == nil {
		t.Fatal("PublishResult accepted an unknown shot")
	}
}
