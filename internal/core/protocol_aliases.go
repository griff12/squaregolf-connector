package core

import "github.com/brentyates/squaregolf-connector/internal/core/protocol"

// This file re-exports the pure protocol layer (internal/core/protocol) under
// the core package's original names. The wire-format parsers, command encoders,
// and protocol enums physically live in the protocol package now; these aliases
// keep existing core.* references working while callers migrate to importing
// protocol directly.

// Protocol value types.
type (
	HandednessType      = protocol.HandednessType
	DetectBallMode      = protocol.DetectBallMode
	SpinMode            = protocol.SpinMode
	ClubType            = protocol.ClubType
	ShotType            = protocol.ShotType
	LaunchMonitorStatus = protocol.LaunchMonitorStatus
	DeviceType          = protocol.DeviceType
	SensorData          = protocol.SensorData
	BallMetrics         = protocol.BallMetrics
	ClubMetrics         = protocol.ClubMetrics
	AlignmentData       = protocol.AlignmentData
)

// Protocol enum values.
const (
	RightHanded = protocol.RightHanded
	LeftHanded  = protocol.LeftHanded

	Deactivate            = protocol.Deactivate
	Activate              = protocol.Activate
	ActivateAlignmentMode = protocol.ActivateAlignmentMode

	Standard = protocol.Standard
	Advanced = protocol.Advanced

	ShotTypeFull = protocol.ShotTypeFull
	ShotTypePutt = protocol.ShotTypePutt

	LaunchMonitorStatusNone   = protocol.LaunchMonitorStatusNone
	LaunchMonitorStatusIdle   = protocol.LaunchMonitorStatusIdle
	LaunchMonitorStatusInit   = protocol.LaunchMonitorStatusInit
	LaunchMonitorStatusDetect = protocol.LaunchMonitorStatusDetect
	LaunchMonitorStatusReady  = protocol.LaunchMonitorStatusReady
	LaunchMonitorStatusShot   = protocol.LaunchMonitorStatusShot
	LaunchMonitorStatusDone   = protocol.LaunchMonitorStatusDone

	DeviceTypeUnknown = protocol.DeviceTypeUnknown
	DeviceTypeHome    = protocol.DeviceTypeHome
	DeviceTypeOmni    = protocol.DeviceTypeOmni

	OmniManufacturerDataHex = protocol.OmniManufacturerDataHex
)

// Club constants.
var (
	ClubPutter         = protocol.ClubPutter
	ClubDriver         = protocol.ClubDriver
	ClubWood3          = protocol.ClubWood3
	ClubWood5          = protocol.ClubWood5
	ClubWood7          = protocol.ClubWood7
	ClubIron4          = protocol.ClubIron4
	ClubIron5          = protocol.ClubIron5
	ClubIron6          = protocol.ClubIron6
	ClubIron7          = protocol.ClubIron7
	ClubIron8          = protocol.ClubIron8
	ClubIron9          = protocol.ClubIron9
	ClubPitchingWedge  = protocol.ClubPitchingWedge
	ClubApproachWedge  = protocol.ClubApproachWedge
	ClubSandWedge      = protocol.ClubSandWedge
	ClubAlignmentStick = protocol.ClubAlignmentStick
)

// Device-type detection.
var DetectDeviceType = protocol.DetectDeviceType

// Parsers.
var (
	ParseSensorData              = protocol.ParseSensorData
	ParseShotBallMetrics         = protocol.ParseShotBallMetrics
	ApplyOmniBallValidityBitmask = protocol.ApplyOmniBallValidityBitmask
	ParseShotClubMetrics         = protocol.ParseShotClubMetrics
	ParseOmniShotClubMetrics     = protocol.ParseOmniShotClubMetrics
	ParseAlignmentData           = protocol.ParseAlignmentData
)

// Command encoders.
var (
	HeartbeatCommand                      = protocol.HeartbeatCommand
	DetectBallCommand                     = protocol.DetectBallCommand
	ClubCommand                           = protocol.ClubCommand
	OmniClubCommand                       = protocol.OmniClubCommand
	SwingStickCommand                     = protocol.SwingStickCommand
	AlignmentCommand                      = protocol.AlignmentCommand
	StartAlignmentCommand                 = protocol.StartAlignmentCommand
	StopAlignmentCommand                  = protocol.StopAlignmentCommand
	CancelAlignmentCommand                = protocol.CancelAlignmentCommand
	RequestClubMetricsCommand             = protocol.RequestClubMetricsCommand
	GetOSVersionCommand                   = protocol.GetOSVersionCommand
	GetChargeCommand                      = protocol.GetChargeCommand
	OmniSetUnitsCommand                   = protocol.OmniSetUnitsCommand
	OmniSetGreenSpeedCommand              = protocol.OmniSetGreenSpeedCommand
	OmniSetCarryDistanceAdjustmentCommand = protocol.OmniSetCarryDistanceAdjustmentCommand
	OmniSetHandedCommand                  = protocol.OmniSetHandedCommand
)
