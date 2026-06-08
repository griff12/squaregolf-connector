package protocol

import "testing"

func TestProfileForResolvesByType(t *testing.T) {
	if got := ProfileFor(DeviceTypeOmni).Type(); got != DeviceTypeOmni {
		t.Errorf("ProfileFor(Omni).Type() = %v, want omni", got)
	}
	if got := ProfileFor(DeviceTypeHome).Type(); got != DeviceTypeHome {
		t.Errorf("ProfileFor(Home).Type() = %v, want home", got)
	}
	// Unknown falls back to Home.
	if got := ProfileFor(DeviceTypeUnknown).Type(); got != DeviceTypeHome {
		t.Errorf("ProfileFor(Unknown).Type() = %v, want home fallback", got)
	}
}

func TestProfileClubCommandDiffersByDevice(t *testing.T) {
	home := ProfileFor(DeviceTypeHome).ClubCommand(0, ClubDriver, RightHanded)
	omni := ProfileFor(DeviceTypeOmni).ClubCommand(0, ClubDriver, RightHanded)
	if home == omni {
		t.Errorf("expected Home and Omni club encodings to differ, both = %q", home)
	}
	if want := ClubCommand(0, ClubDriver, RightHanded); home != want {
		t.Errorf("home club command = %q, want %q", home, want)
	}
	if want := OmniClubCommand(0, ClubDriver, RightHanded); omni != want {
		t.Errorf("omni club command = %q, want %q", omni, want)
	}
}

func TestHomeProfileHasNoInitCommands(t *testing.T) {
	seq := func() int { return 0 }
	if got := ProfileFor(DeviceTypeHome).InitCommands(InitOptions{}, seq); got != nil {
		t.Errorf("home InitCommands = %v, want nil", got)
	}
}

func TestOmniInitCommandsIncludeHandedWhenSet(t *testing.T) {
	n := 0
	seq := func() int { n++; return n }
	hand := LeftHanded

	withHand := ProfileFor(DeviceTypeOmni).InitCommands(InitOptions{Handedness: &hand}, seq)
	if len(withHand) != 4 {
		t.Fatalf("omni InitCommands with handedness = %d commands, want 4", len(withHand))
	}
	if withHand[3].Name != "SetHanded" {
		t.Errorf("last command = %q, want SetHanded", withHand[3].Name)
	}

	withoutHand := ProfileFor(DeviceTypeOmni).InitCommands(InitOptions{}, seq)
	if len(withoutHand) != 3 {
		t.Fatalf("omni InitCommands without handedness = %d commands, want 3", len(withoutHand))
	}
}
