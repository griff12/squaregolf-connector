//go:build android

package core

import "errors"

// Android counterpart of tinygo_bluetooth_client.go, which carries
// //go:build !android because it is the only file in the repo importing
// tinygo.org/x/bluetooth. That package binds to BlueZ, CoreBluetooth and WinRT;
// Android has none of them. internal/core is a single Go package and Go links
// at package granularity, so leaving that file untagged makes the whole package
// -- launch_monitor.go, state_manager.go, both test doubles -- unbuildable for
// android/arm64.
//
// Nothing here talks to a radio. On Android the BLE transport is supplied by
// Kotlin and installed with BluetoothManager.SetClient before
// GetLaunchMonitorInstance is first called. Every declaration below exists only
// so bluetooth_manager.go still compiles.

// ConnectionPhase mirrors the non-android declaration so the phase switch in
// bluetooth_manager.go still type-checks. The values must stay identical.
type ConnectionPhase string

const (
	PhaseScanning   ConnectionPhase = "scanning"
	PhaseConnecting ConnectionPhase = "connecting"
)

// globalAdapter mirrors the *bluetooth.Adapter singleton of the non-android
// build so the "if globalAdapter != nil" guard in bluetooth_manager.go still
// compiles. It is declared as an interface type because that guard requires a
// nilable type; it is always nil here.
var globalAdapter any

// releaseAdapter is a no-op on android: there is no native adapter to release.
func releaseAdapter() {}

// NewTinyGoBluetoothClient always fails on android. The return type is the
// interface rather than a concrete type because there is no android
// implementation to name; the two files never coexist, so the differing
// signature is invisible to every caller.
//
// Both callers -- the nil-client fallback in bluetooth_manager.go and main.go
// -- check the error before touching the client, so the error branch is the
// only reachable path and no typed-nil ever enters a BluetoothClient value.
func NewTinyGoBluetoothClient() (BluetoothClient, error) {
	return nil, errors.New("no built-in Bluetooth client on android: install one with BluetoothManager.SetClient first")
}
