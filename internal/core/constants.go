package core

// ConnectionStatus represents the current state of the Bluetooth connection
type ConnectionStatus string

const (
	ConnectionStatusDisconnected ConnectionStatus = "disconnected"
	ConnectionStatusScanning     ConnectionStatus = "scanning"
	ConnectionStatusConnecting   ConnectionStatus = "connecting"
	ConnectionStatusConnected    ConnectionStatus = "connected"
	ConnectionStatusError        ConnectionStatus = "error"
)

// MockMode represents the type of mock implementation to use
type MockMode string

const (
	MockModeNone     MockMode = ""         // No mock, use real implementation
	MockModeStub     MockMode = "stub"     // Basic mock implementation
	MockModeSimulate MockMode = "simulate" // Simulated device with realistic behavior
)

// DeviceState represents the current state of the simulated device
type DeviceState string

const (
	DeviceStateIdle          DeviceState = "idle"
	DeviceStateBallDetection DeviceState = "ball_detection"
	DeviceStateReady         DeviceState = "ball_ready"
)

// BallState represents the current state of the ball in the simulator
type BallState int

const (
	BallStateNone BallState = iota
	BallStateDetected
	BallStateReady
)

// BLE Characteristic UUIDs
const (
	CommandCharUUID         = "86602101-6b7e-439a-bdd1-489a3213e9bb"
	NotificationCharUUID    = "86602102-6b7e-439a-bdd1-489a3213e9bb"
	BatteryLevelCharUUID    = "00002a19-0000-1000-8000-00805f9b34fb"
	FirmwareVersionCharUUID = "86602003-6b7e-439a-bdd1-489a3213e9bb"
	SerialNumberCharUUID    = "86602001-6b7e-439a-bdd1-489a3213e9bb"
)

const (
	// AppName is the consistent name used for directories and files
	AppName = "SquareGolf Connector"
	// AppDirName is the sanitized version of AppName for use in paths
	AppDirName = "squaregolf-connector"

	// WindowTitle is the title shown in the window title bar
	WindowTitle = AppName + " - Unofficial Launch Monitor Connector"

	// Navigation screen names
	ScreenDevice    = "Device"
	ScreenAlignment = "Alignment"
	ScreenGSPro     = "GSPro"
	ScreenRange     = "Range"
	ScreenSettings  = "Settings"

	// BluetoothDevicePrefix is the prefix used to identify SquareGolf devices
	BluetoothDevicePrefix = "SquareGolf"
)
