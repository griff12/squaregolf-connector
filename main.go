package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	appcfg "github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/logging"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/brentyates/squaregolf-connector/internal/plugins/camera"
	"github.com/brentyates/squaregolf-connector/internal/plugins/connectapi"
	"github.com/brentyates/squaregolf-connector/internal/ui"
	"github.com/brentyates/squaregolf-connector/internal/web"
)

// Application configuration
type AppConfig struct {
	UseMock              core.MockMode
	DeviceName           string
	Headless             bool
	WebMode              bool
	ServerOnly           bool
	WebPort              int
	GSProIP              string
	GSProPort            int
	EnableGSPro          bool
	EnableExternalCamera bool
	SimulateOmni         bool
}

// seedIntegrationConfig applies a plugin's persisted generic config, falling
// back to legacy typed settings when none has been saved yet.
func seedIntegrationConfig(registry *plugin.Registry, name string, fallback map[string]any) {
	cs, ok := registry.ConfigStore(name)
	if !ok {
		return
	}
	cfg := appcfg.GetInstance().GetIntegrationConfig(name)
	if cfg == nil {
		cfg = fallback
	}
	cs.Configure(cfg)
}

func legacyGSProConfig(config AppConfig, settings appcfg.Settings) map[string]any {
	host := settings.GSProIP
	port := settings.GSProPort
	if config.EnableGSPro {
		// Explicit CLI connection settings take precedence over saved legacy
		// values, matching the pre-plugin behavior.
		host = config.GSProIP
		port = config.GSProPort
	}
	return map[string]any{
		"host": host, "port": port, "autoConnect": settings.GSProAutoConnect,
	}
}

// Initialize the backend services (Bluetooth, state manager, etc.)
func initializeBackend(config AppConfig) (*core.StateManager, *core.BluetoothManager, *core.LaunchMonitor) {
	// Initialize logging
	logging.SetAppName(core.AppName)
	if err := logging.Init(); err != nil {
		os.Exit(1)
	}
	log.Println("Starting Square BT application...")

	// Get the state manager instance
	stateManager := core.GetInstance()

	// Create the appropriate Bluetooth client
	var bleClient core.BluetoothClient
	var err error

	if config.UseMock == core.MockModeStub {
		log.Println("Using mock Bluetooth implementation")
		bleClient = core.NewMockBluetoothClient()
	} else if config.UseMock == core.MockModeSimulate {
		log.Println("Using simulated device implementation")
		simulatorConfig := core.SimulatorConfig{
			BatteryDrainRate: 1,
			ResponseDelay:    100 * time.Millisecond,
			SimulateOmni:     config.SimulateOmni,
		}
		bleClient = core.NewSimulatorBluetoothClient(simulatorConfig)
	} else {
		log.Println("Using real Bluetooth implementation with TinyGo")
		bleClient, err = core.NewTinyGoBluetoothClient()
		if err != nil {
			log.Printf("Failed to initialize Bluetooth: %v", err)
			// Exit the application if Bluetooth initialization fails
			os.Exit(1)
		}
	}

	// Get the singleton bluetooth manager instance
	bluetoothManager := core.GetBluetoothInstance(stateManager)

	// Set the bluetooth client on the bluetooth manager
	bluetoothManager.SetClient(bleClient)

	// Get the singleton launch monitor instance
	launchMonitor := core.GetLaunchMonitorInstance(stateManager, bluetoothManager)

	// Set up launch monitor to handle notifications from the bluetooth manager
	launchMonitor.SetupNotifications(bluetoothManager)

	return stateManager, bluetoothManager, launchMonitor
}

// setupHeadlessCallbacks configures callbacks for headless mode
func setupHeadlessCallbacks(stateManager *core.StateManager) {
	stateManager.RegisterConnectionStatusCallback(func(oldValue, newValue core.ConnectionStatus) {
		log.Printf("Connection status changed from %v to %v", oldValue, newValue)
	})

	stateManager.RegisterLastBallMetricsCallback(func(oldValue, newValue *core.BallMetrics) {
		if newValue != nil {
			log.Printf("New ball metrics received: %v", newValue)
		}
	})

	stateManager.RegisterLastClubMetricsCallback(func(oldValue, newValue *core.ClubMetrics) {
		if newValue != nil {
			log.Printf("New club metrics received: %v", newValue)
		}
	})

	stateManager.RegisterBatteryLevelCallback(func(oldValue, newValue *int) {
		if newValue != nil {
			log.Printf("Battery level: %d%%", *newValue)
		}
	})

	stateManager.RegisterDeviceDisplayNameCallback(func(oldValue, newValue *string) {
		if newValue != nil {
			log.Printf("Device name: %s", *newValue)
		}
	})

	stateManager.RegisterClubCallback(func(oldValue, newValue *core.ClubType) {
		if newValue != nil {
			log.Printf("Club changed to: %s", newValue.RegularCode)
		}
	})

	stateManager.RegisterHandednessCallback(func(oldValue, newValue *core.HandednessType) {
		if newValue != nil {
			handedness := "Right"
			if *newValue == core.LeftHanded {
				handedness = "Left"
			}
			log.Printf("Handedness: %s", handedness)
		}
	})

	stateManager.RegisterBallDetectedCallback(func(oldValue, newValue bool) {
		log.Printf("Ball detected: %v", newValue)
	})

	stateManager.RegisterBallReadyCallback(func(oldValue, newValue bool) {
		log.Printf("Ball ready: %v", newValue)
	})

	stateManager.RegisterBallPositionCallback(func(oldValue, newValue *core.BallPosition) {
		if newValue != nil {
			log.Printf("Ball position: X=%d, Y=%d, Z=%d", newValue.X, newValue.Y, newValue.Z)
		}
	})

	stateManager.RegisterLastErrorCallback(func(oldValue, newValue error) {
		if newValue != nil {
			log.Printf("Error: %v", newValue)
		}
	})
}

// startCLI initializes and runs the command-line interface
func startCLI(config AppConfig, stateManager *core.StateManager, bluetoothManager *core.BluetoothManager, launchMonitor *core.LaunchMonitor) {
	// Setup callbacks for headless mode
	setupHeadlessCallbacks(stateManager)

	// Start bluetooth connection
	log.Println("Starting Bluetooth connection...")
	bluetoothManager.StartBluetoothConnection(config.DeviceName, "")

	// Wait for connection to be established
	log.Println("Waiting for Bluetooth connection...")
	connectionTimeout := time.After(10 * time.Second)
	connectionEstablished := make(chan struct{})

	// Register a one-time callback for successful connection
	stateManager.RegisterConnectionStatusCallback(func(oldValue, newValue core.ConnectionStatus) {
		if newValue == core.ConnectionStatusConnected {
			close(connectionEstablished)
		}
	})

	select {
	case <-connectionEstablished:
		log.Println("Bluetooth connection established")
	case <-connectionTimeout:
		log.Println("Timeout waiting for Bluetooth connection")
		bluetoothManager.DisconnectBluetooth()
		return
	}

	// Setup GSPro integration if enabled
	var registry *plugin.Registry
	if config.EnableGSPro {
		log.Println("Starting GSPro integration")
		host := core.NewPluginHost(stateManager, launchMonitor)
		registry = plugin.NewRegistry(host)
		registry.Register(connectapi.New(connectapi.OpenAPI(), config.GSProIP, config.GSProPort))
		registry.StartAll(context.Background())
		if c, ok := registry.Connectable("gspro"); ok {
			go c.BeginConnect(config.GSProIP, config.GSProPort)
		}
	}

	// Wait for interrupt signal to gracefully shut down
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Block until we receive a signal
	<-sigChan
	log.Println("Shutting down...")

	// Clean up
	if registry != nil {
		registry.StopAll()
	}
	bluetoothManager.DisconnectBluetooth()

	// Give everything a moment to clean up
	time.Sleep(1 * time.Second)
	log.Println("Application stopped")
}

// startWebServer initializes and runs the web server
func startWebServer(config AppConfig, stateManager *core.StateManager, bluetoothManager *core.BluetoothManager, launchMonitor *core.LaunchMonitor) {
	// Initialize config manager and load settings (happens behind the scenes like Fyne)
	settings := appcfg.GetInstance().GetSettings()

	// Apply loaded settings to state manager
	appcfg.GetInstance().ApplyToStateManager(stateManager)

	// Composition root: assemble the plugin registry. This is the only place
	// that knows the concrete plugin types. GSPro and InfiniteTees are the same
	// config-driven Connect-API plugin; a third compatible system is one more
	// registry line.
	pluginHost := core.NewPluginHost(stateManager, launchMonitor)
	registry := plugin.NewRegistry(pluginHost)
	registry.Register(connectapi.New(connectapi.OpenAPI(), config.GSProIP, config.GSProPort))
	if config.EnableExternalCamera {
		registry.Register(camera.New(camera.NewSwingCamVendor(settings.CameraURL), settings.CameraURL, settings.CameraEnabled))
	}

	// Seed each plugin's config from persisted generic config, falling back to
	// the legacy typed settings so existing installs keep their values.
	seedIntegrationConfig(registry, "gspro", legacyGSProConfig(config, settings))
	seedIntegrationConfig(registry, "camera", map[string]any{
		"url": settings.CameraURL, "enabled": settings.CameraEnabled,
	})

	registry.StartAll(context.Background())

	// Create web server over the assembled registry
	server := web.NewServer(stateManager, bluetoothManager, launchMonitor, registry)

	// Auto-connect any Connectable plugin whose config asks for it.
	for _, name := range registry.Names() {
		c, ok := registry.Connectable(name)
		if !ok {
			continue
		}
		cfg := map[string]any{}
		if cs, ok := registry.ConfigStore(name); ok {
			cfg = cs.Config()
		}
		auto, _ := cfg["autoConnect"].(bool)
		if config.EnableGSPro && name == "gspro" {
			auto = true
		}
		if !auto {
			continue
		}
		addr, _ := cfg["host"].(string)
		port, _ := cfg["port"].(int)
		log.Printf("Auto-connecting %s at %s:%d", name, addr, port)
		go c.BeginConnect(addr, port)
	}

	log.Printf("Auto-connecting to device: %s", settings.DeviceName)
	bluetoothManager.StartBluetoothConnection(settings.DeviceName, "")

	// Set up graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Start the web server in a goroutine
	var stopOnce sync.Once
	stopServer := func() {
		stopOnce.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			if err := server.Stop(ctx); err != nil {
				log.Printf("Warning: Could not stop web server cleanly: %v", err)
			}

			registry.StopAll()
			bluetoothManager.DisconnectBluetooth()
		})
	}

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("Starting web server on http://localhost:%d", config.WebPort)
		if err := server.Start(config.WebPort); err != nil {
			serverErr <- err
		}
	}()

	// Give the server a moment to start up
	time.Sleep(500 * time.Millisecond)

	if config.ServerOnly {
		select {
		case <-sigChan:
			log.Println("Shutting down web server...")
			stopServer()
		case err := <-serverErr:
			stopServer()
			log.Fatal(err)
		}
		return
	}

	window := ui.NewDesktopWindow(fmt.Sprintf("http://localhost:%d", config.WebPort))

	exitErr := make(chan error, 1)
	go func() {
		select {
		case <-sigChan:
			log.Println("Shutting down web server...")
			stopServer()
			window.Terminate()
		case err := <-serverErr:
			exitErr <- fmt.Errorf("web server failed to start: %w", err)
			stopServer()
			window.Terminate()
		}
	}()

	window.Run()
	log.Println("Desktop window closed, shutting down...")
	stopServer()

	select {
	case err := <-exitErr:
		log.Fatal(err)
	default:
	}
}

func main() {
	// Parse command line flags
	useMock := flag.String("mock", "", "Mock mode: 'stub' for basic mock, 'simulate' for simulated device with realistic behavior, or empty for real hardware")
	deviceName := flag.String("device", "", "Name of the Bluetooth device to connect to")
	headless := flag.Bool("headless", false, "Run in headless CLI mode without UI")
	serverOnly := flag.Bool("server-only", false, "Run the web server without opening the desktop window")
	webPort := flag.Int("web-port", 8080, "Port for web server")
	gsproIP := flag.String("gspro-ip", "127.0.0.1", "IP address of GSPro server")
	gsproPort := flag.Int("gspro-port", 921, "Port of GSPro server")
	enableGSPro := flag.Bool("enable-gspro", false, "Enable GSPro integration")
	enableExternalCamera := flag.Bool("enable-external-camera", false, "Enable external camera integration (experimental)")
	simulateOmni := flag.Bool("omni", false, "Simulate an Omni device instead of Home (requires --mock simulate)")
	flag.Parse()

	config := AppConfig{
		UseMock:              core.MockMode(*useMock),
		DeviceName:           *deviceName,
		Headless:             *headless,
		WebMode:              !*headless,
		ServerOnly:           *serverOnly,
		WebPort:              *webPort,
		GSProIP:              *gsproIP,
		GSProPort:            *gsproPort,
		EnableGSPro:          *enableGSPro,
		EnableExternalCamera: *enableExternalCamera,
		SimulateOmni:         *simulateOmni,
	}

	// Initialize common backend components
	stateManager, bluetoothManager, launchMonitor := initializeBackend(config)

	// Launch the appropriate interface based on mode
	if config.Headless {
		startCLI(config, stateManager, bluetoothManager, launchMonitor)
	} else {
		startWebServer(config, stateManager, bluetoothManager, launchMonitor)
	}
}
