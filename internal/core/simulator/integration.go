package simulator

// Integration is the lifecycle surface that a launch-monitor software
// integration (GSPro, InfiniteTees, and future sims) exposes to the application.
// Concrete integrations satisfy it through the embedded *Base plus their own
// Name(), so the web layer can drive any registered integration generically
// without depending on its concrete type — the open/closed seam for adding sims.
type Integration interface {
	Name() string
	GetConnectionInfo() (host string, port int)
	Start()
	Stop()
	Connect(host string, port int)
	Disconnect()
	EnableAutoReconnect()
	DisableAutoReconnect()
	ResetReconnectionState()
}
