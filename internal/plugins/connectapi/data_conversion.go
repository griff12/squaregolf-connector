package connectapi

import (
	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
)

// convertToShotFormat converts internal shot data format to GSPro format
func (g *Integration) convertToShotFormat(ballMetrics protocol.BallMetrics, incrementShot bool) ShotData {
	// Increment shot number only when requested (for new ball data)
	if incrementShot {
		g.shotNumber++
		g.lastShotNumber = g.shotNumber
	}

	return ShotData{
		DeviceID:   "CustomLaunchMonitor",
		Units:      "Yards",
		APIversion: "1",
		ShotNumber: g.lastShotNumber,
		ShotDataOptions: ShotOptions{
			ContainsBallData: true,
			ContainsClubData: false,
		},
		BallData: &BallData{
			Speed:     ballMetrics.BallSpeedMPS * 2.23694, // Convert m/s to mph
			SpinAxis:  ballMetrics.SpinAxis * -1,
			TotalSpin: ballMetrics.TotalspinRPM,
			BackSpin:  ballMetrics.BackspinRPM,
			SideSpin:  ballMetrics.SidespinRPM * -1,
			HLA:       ballMetrics.HorizontalAngle,
			VLA:       ballMetrics.VerticalAngle,
		},
		// Deliberately nil, not &ClubData{}: ShotData tags this field omitempty,
		// which only skips a NIL pointer. An empty struct still marshals, so every
		// ball message carried ten zeroed club fields alongside
		// ContainsClubData:false - contradicting itself and wasting the payload.
		//
		// Both club-data paths in listeners.go assign ClubData themselves before
		// sending, so nothing downstream depends on this being non-nil.
		ClubData: nil,
	}
}

// convertClubData converts internal club data format to GSPro format
func (g *Integration) convertClubData(clubMetrics protocol.ClubMetrics) *ClubData {
	return &ClubData{
		Speed:                clubMetrics.ClubSpeed * 2.23694,
		AngleOfAttack:        clubMetrics.AttackAngle,
		FaceToTarget:         clubMetrics.FaceAngle,
		Lie:                  0,
		Loft:                 clubMetrics.DynamicLoftAngle,
		Path:                 clubMetrics.PathAngle,
		SpeedAtImpact:        0,
		VerticalFaceImpact:   clubMetrics.ImpactVertical,
		HorizontalFaceImpact: clubMetrics.ImpactHorizontal,
		ClosureRate:          0,
	}
}

// mapClubToInternal maps GSPro club name to internal ClubType
func (g *Integration) mapClubToInternal(clubName string) *protocol.ClubType {
	// Map GSPro club names to our internal ClubType
	clubMap := map[string]protocol.ClubType{
		// Drivers and woods
		"DR": protocol.ClubDriver,
		"W2": protocol.ClubWood3,
		"W3": protocol.ClubWood3,
		"W4": protocol.ClubWood5,
		"W5": protocol.ClubWood5,
		"W6": protocol.ClubWood7,
		"W7": protocol.ClubWood7,

		// Hybrids
		"H2": protocol.ClubWood3,
		"H3": protocol.ClubWood3,
		"H4": protocol.ClubWood3,
		"H5": protocol.ClubWood3,
		"H6": protocol.ClubWood5,
		"H7": protocol.ClubIron4,

		// Irons
		"I1": protocol.ClubWood3,
		"I2": protocol.ClubWood3,
		"I3": protocol.ClubWood5,
		"I4": protocol.ClubIron4,
		"I5": protocol.ClubIron5,
		"I6": protocol.ClubIron6,
		"I7": protocol.ClubIron7,
		"I8": protocol.ClubIron8,
		"I9": protocol.ClubIron9,

		// Wedges
		"PW": protocol.ClubPitchingWedge,
		"AW": protocol.ClubApproachWedge,
		"GW": protocol.ClubApproachWedge,
		"SW": protocol.ClubSandWedge,
		"LW": protocol.ClubSandWedge,

		// Putter
		"PT": protocol.ClubPutter,
	}

	if club, ok := clubMap[clubName]; ok {
		return &club
	}
	return nil
}

// mapClubToFriendlyName converts GSPro club codes to short readable names for display
func mapClubToFriendlyName(clubCode string) string {
	nameMap := map[string]string{
		// Drivers and woods
		"DR": "DR",
		"W2": "2W",
		"W3": "3W",
		"W4": "4W",
		"W5": "5W",
		"W6": "6W",
		"W7": "7W",

		// Hybrids
		"H2": "2H",
		"H3": "3H",
		"H4": "4H",
		"H5": "5H",
		"H6": "6H",
		"H7": "7H",

		// Irons
		"I1": "1I",
		"I2": "2I",
		"I3": "3I",
		"I4": "4I",
		"I5": "5I",
		"I6": "6I",
		"I7": "7I",
		"I8": "8I",
		"I9": "9I",

		// Wedges
		"PW": "PW",
		"AW": "AW",
		"GW": "GW",
		"SW": "SW",
		"LW": "LW",

		// Putter
		"PT": "PUTT",
	}

	if name, ok := nameMap[clubCode]; ok {
		return name
	}
	// Return the code itself if no mapping found
	return clubCode
}
