package protocol

// DeviceProfile encapsulates the wire-level behavior that varies between
// SquareGolf device models. It is the open/closed seam for adding a new device:
// implement this interface in a new value, register it in profiles, and the
// launch monitor drives it without further edits.
//
// Only pure, transport-agnostic decisions belong here (command encoding,
// notification parsing, init sequencing). Stateful orchestration (retry timers,
// putter filters that read live club state) stays in the launch monitor.
type DeviceProfile interface {
	// Type returns the device variant this profile serves.
	Type() DeviceType
	// ClubCommand encodes a club-selection command for this device.
	ClubCommand(sequence int, club ClubType, handedness HandednessType) string
	// ParseClubMetrics decodes a club-metrics notification for this device.
	ParseClubMetrics(bytesList []string) (*ClubMetrics, error)
	// ApplyBallValidity applies device-specific validity post-processing to a
	// parsed ball-metrics value (e.g. the Omni validity bitmask).
	ApplyBallValidity(metrics *BallMetrics)
	// InitCommands returns the ordered commands to send after connection. Home
	// devices need none.
	InitCommands(opts InitOptions, nextSequence func() int) []NamedCommand
}

// InitOptions carries the device-configuration values (resolved from app state)
// needed to build a device's post-connect init sequence.
type InitOptions struct {
	SpeedUnit       int
	DistanceUnit    int
	CarryAdjustment int
	GreenSpeed      int
	Handedness      *HandednessType
}

// NamedCommand is a command paired with a human-readable label for logging.
type NamedCommand struct {
	Name string
	Hex  string
}

// profiles is the registry of known device profiles. Adding a device model is
// additive: append a new profile here.
var profiles = []DeviceProfile{
	omniProfile{},
	homeProfile{},
}

// ProfileFor returns the DeviceProfile for a device type, defaulting to the
// Home profile for unknown devices.
func ProfileFor(deviceType DeviceType) DeviceProfile {
	for _, p := range profiles {
		if p.Type() == deviceType {
			return p
		}
	}
	return homeProfile{}
}

// homeProfile is the default SquareGolf Home device behavior.
type homeProfile struct{}

func (homeProfile) Type() DeviceType { return DeviceTypeHome }

func (homeProfile) ClubCommand(sequence int, club ClubType, handedness HandednessType) string {
	return ClubCommand(sequence, club, handedness)
}

func (homeProfile) ParseClubMetrics(bytesList []string) (*ClubMetrics, error) {
	return ParseShotClubMetrics(bytesList)
}

func (homeProfile) ApplyBallValidity(metrics *BallMetrics) {}

func (homeProfile) InitCommands(opts InitOptions, nextSequence func() int) []NamedCommand {
	return nil
}

// omniProfile is the SquareGolf Omni device behavior.
type omniProfile struct{}

func (omniProfile) Type() DeviceType { return DeviceTypeOmni }

func (omniProfile) ClubCommand(sequence int, club ClubType, handedness HandednessType) string {
	return OmniClubCommand(sequence, club, handedness)
}

func (omniProfile) ParseClubMetrics(bytesList []string) (*ClubMetrics, error) {
	return ParseOmniShotClubMetrics(bytesList)
}

func (omniProfile) ApplyBallValidity(metrics *BallMetrics) {
	ApplyOmniBallValidityBitmask(metrics)
}

func (omniProfile) InitCommands(opts InitOptions, nextSequence func() int) []NamedCommand {
	commands := []NamedCommand{
		{"SetUnits", OmniSetUnitsCommand(nextSequence(), opts.SpeedUnit, opts.DistanceUnit)},
		{"SetCarryDistanceAdjustment", OmniSetCarryDistanceAdjustmentCommand(nextSequence(), opts.CarryAdjustment)},
		{"SetGreenSpeed", OmniSetGreenSpeedCommand(nextSequence(), opts.GreenSpeed)},
	}
	if opts.Handedness != nil {
		commands = append(commands, NamedCommand{"SetHanded", OmniSetHandedCommand(nextSequence(), *opts.Handedness)})
	}
	return commands
}
