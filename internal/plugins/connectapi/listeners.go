package connectapi

import (
	"log"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
)

func (g *Integration) onBallReadyChanged(oldValue, newValue bool) {
	if oldValue == newValue {
		return
	}

	if !g.Base.Connected || g.Base.Socket == nil {
		return
	}

	emptyShotData := ShotData{
		DeviceID:   "CustomLaunchMonitor",
		Units:      "Yards",
		APIversion: "1",
		ShotNumber: g.lastShotNumber,
		ShotDataOptions: ShotOptions{
			ContainsBallData:          false,
			ContainsClubData:          false,
			LaunchMonitorIsReady:      newValue,
			LaunchMonitorBallDetected: newValue,
		},
	}

	if err := g.sendData(emptyShotData); err != nil {
		log.Printf("[%s] Error sending empty shot data: %v", g.cfg.DisplayName, err)
	}
}

func (g *Integration) onLastBallMetricsChanged(oldValue, newValue *protocol.BallMetrics) {
	if oldValue == newValue {
		return
	}

	if !g.Base.Connected || g.Base.Socket == nil {
		return
	}

	if newValue == nil {
		return
	}

	gsproShotData := g.convertToShotFormat(*newValue, true)
	if err := g.sendData(gsproShotData); err != nil {
		log.Printf("[%s] Error sending shot data: %v", g.cfg.DisplayName, err)
	}
}

func (g *Integration) onLastClubMetricsChanged(oldValue, newValue *protocol.ClubMetrics) {
	if oldValue == newValue {
		return
	}

	if !g.Base.Connected || g.Base.Socket == nil {
		return
	}

	if newValue == nil {
		zeroedClubData := &ClubData{
			Speed:                0,
			AngleOfAttack:        0,
			FaceToTarget:         0,
			Lie:                  0,
			Loft:                 0,
			Path:                 0,
			SpeedAtImpact:        0,
			VerticalFaceImpact:   0,
			HorizontalFaceImpact: 0,
			ClosureRate:          0,
		}

		gsproShotData := g.convertToShotFormat(protocol.BallMetrics{}, false)
		gsproShotData.ShotDataOptions.ContainsBallData = false
		gsproShotData.ShotDataOptions.ContainsClubData = true
		gsproShotData.ClubData = zeroedClubData
		if err := g.sendData(gsproShotData); err != nil {
			log.Printf("[%s] Error sending zeroed club data: %v", g.cfg.DisplayName, err)
		}
		return
	}

	gsproShotData := g.convertToShotFormat(protocol.BallMetrics{}, false)
	gsproShotData.ShotDataOptions.ContainsBallData = false
	gsproShotData.ShotDataOptions.ContainsClubData = true
	gsproShotData.ClubData = g.convertClubData(*newValue)
	if err := g.sendData(gsproShotData); err != nil {
		log.Printf("[%s] Error sending club data: %v", g.cfg.DisplayName, err)
	}
}
