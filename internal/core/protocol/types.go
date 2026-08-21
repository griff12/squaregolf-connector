// Package protocol is the pure SquareGolf wire-format layer: it decodes device
// notification bytes into domain metrics and encodes commands into bytes. It has
// no dependency on Bluetooth transport, application state, or integrations, so
// it can be unit-tested in isolation and reused across device variants.
package protocol

import "strings"

// HandednessType represents player handedness
type HandednessType int

const (
	RightHanded HandednessType = iota
	LeftHanded
)

// DetectBallMode represents ball detection mode
type DetectBallMode int

const (
	Deactivate            DetectBallMode = iota // 0 = deactivate ball detection
	Activate                                    // 1 = activate ball detection (standard mode)
	ActivateAlignmentMode                       // 2 = activate in alignment mode
)

// SpinMode represents spin measurement mode
type SpinMode int

const (
	Standard SpinMode = iota
	Advanced
)

// ClubType represents the different types of golf clubs
type ClubType struct {
	RegularCode    string
	SwingStickCode string
}

// Club types as constants
var (
	// Putter
	ClubPutter = ClubType{RegularCode: "0107", SwingStickCode: "0103"}

	// Drivers and woods
	ClubDriver = ClubType{RegularCode: "0204", SwingStickCode: "0202"}
	ClubWood3  = ClubType{RegularCode: "0305", SwingStickCode: "0301"}
	ClubWood5  = ClubType{RegularCode: "0505", SwingStickCode: "0501"}
	ClubWood7  = ClubType{RegularCode: "0705", SwingStickCode: "0701"}

	// Irons
	ClubIron4 = ClubType{RegularCode: "0406", SwingStickCode: "0400"}
	ClubIron5 = ClubType{RegularCode: "0506", SwingStickCode: "0500"}
	ClubIron6 = ClubType{RegularCode: "0606", SwingStickCode: "0600"}
	ClubIron7 = ClubType{RegularCode: "0706", SwingStickCode: "0700"}
	ClubIron8 = ClubType{RegularCode: "0806", SwingStickCode: "0900"}
	ClubIron9 = ClubType{RegularCode: "0906", SwingStickCode: "0900"}

	// Wedges
	ClubPitchingWedge = ClubType{RegularCode: "0a06", SwingStickCode: "0a00"}
	ClubApproachWedge = ClubType{RegularCode: "0b06", SwingStickCode: "0b00"}
	ClubSandWedge     = ClubType{RegularCode: "0c06", SwingStickCode: "0c00"}

	// Alignment stick - special club type used to activate alignment mode
	ClubAlignmentStick = ClubType{RegularCode: "0008", SwingStickCode: "0008"}
)

// ShotType represents the type of shot
type ShotType string

const (
	ShotTypeFull ShotType = "full"
	ShotTypePutt ShotType = "putt"
)

// LaunchMonitorStatus represents the current device status reported by 11 03 packets.
type LaunchMonitorStatus string

const (
	LaunchMonitorStatusNone   LaunchMonitorStatus = "none"
	LaunchMonitorStatusIdle   LaunchMonitorStatus = "idle"
	LaunchMonitorStatusInit   LaunchMonitorStatus = "init"
	LaunchMonitorStatusDetect LaunchMonitorStatus = "detect"
	LaunchMonitorStatusReady  LaunchMonitorStatus = "ready"
	LaunchMonitorStatusShot   LaunchMonitorStatus = "shot"
	LaunchMonitorStatusDone   LaunchMonitorStatus = "done"
)

// DeviceType represents the type of Square Golf launch monitor
type DeviceType string

const (
	DeviceTypeUnknown DeviceType = "unknown"
	DeviceTypeHome    DeviceType = "home"
	DeviceTypeOmni    DeviceType = "omni"
)

const OmniManufacturerDataHex = "3033303041"

// DetectDeviceType infers the device variant from its advertised manufacturer data.
func DetectDeviceType(mfgDataHex string) DeviceType {
	if len(mfgDataHex) > 0 && strings.Contains(strings.ToUpper(mfgDataHex), strings.ToUpper(OmniManufacturerDataHex)) {
		return DeviceTypeOmni
	}
	return DeviceTypeHome
}
