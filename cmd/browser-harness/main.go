package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	appcfg "github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/brentyates/squaregolf-connector/internal/plugins/connectapi"
	"github.com/brentyates/squaregolf-connector/internal/web"
)

type demoFeedbackPlugin struct {
	sub plugin.Subscription
}

func (p *demoFeedbackPlugin) Name() string { return "demo-feedback" }

func (p *demoFeedbackPlugin) Start(_ context.Context, host plugin.Host) error {
	p.sub = host.OnShot(func(shot plugin.Shot) {
		min, max := -5.0, 5.0
		if err := host.PublishResult(plugin.Result{
			Plugin:        p.Name(),
			Kind:          "swing.feedback",
			CorrelationID: shot.ID,
			Summary:       "Your connected integrations will collect around this shot.",
			Metrics: []plugin.Metric{
				{Key: "example-impact", Label: "Example impact", Value: 3.2, Unit: "deg", Phase: "impact", Status: "in-range", TargetMin: &min, TargetMax: &max},
			},
			Insights: []plugin.Insight{
				{Key: "example-tempo", Title: "Balanced motion", Message: "This harness result demonstrates interpreted plugin feedback.", Severity: "info", Recommendation: "Keep the same tempo on the next swing."},
			},
			Series: []plugin.Series{
				{Key: "example-curve", Label: "Example motion curve", Unit: "deg", Points: []float64{-2, -1, 1, 4, 3, 1}},
			},
		}); err != nil {
			log.Printf("demo feedback publish failed: %v", err)
		}
	})
	return nil
}

func (p *demoFeedbackPlugin) Stop() error {
	if p.sub != nil {
		p.sub.Close()
	}
	return nil
}

func main() {
	mockMode := flag.String("mock", "simulate", "Mock mode to use for the browser harness")
	port := flag.Int("port", 8091, "Port for the browser harness web server")
	omni := flag.Bool("omni", false, "Simulate an Omni device instead of Home")
	flag.Parse()

	log.Printf("Browser harness starting with mock mode %q on port %d", *mockMode, *port)

	stateManager := core.GetInstance()
	settings := appcfg.GetInstance().GetSettings()
	appcfg.GetInstance().ApplyToStateManager(stateManager)

	var bleClient core.BluetoothClient
	switch core.MockMode(*mockMode) {
	case core.MockModeStub:
		bleClient = core.NewMockBluetoothClient()
	case core.MockModeSimulate:
		bleClient = core.NewSimulatorBluetoothClient(core.SimulatorConfig{
			BatteryDrainRate: 1,
			ResponseDelay:    100 * time.Millisecond,
			SimulateOmni:     *omni,
		})
	default:
		log.Fatalf("unsupported mock mode for browser harness: %q", *mockMode)
	}

	bluetoothManager := core.GetBluetoothInstance(stateManager)
	bluetoothManager.SetClient(bleClient)
	launchMonitor := core.GetLaunchMonitorInstance(stateManager, bluetoothManager)
	launchMonitor.SetupNotifications(bluetoothManager)

	registry := plugin.NewRegistry(core.NewPluginHost(stateManager, launchMonitor))
	registry.Register(connectapi.New(connectapi.OpenAPI(), settings.GSProIP, settings.GSProPort))
	registry.Register(&demoFeedbackPlugin{})
	registry.StartAll(context.Background())

	server := web.NewServer(
		stateManager,
		bluetoothManager,
		launchMonitor,
		registry,
	)

	bluetoothManager.StartBluetoothConnection(settings.DeviceName, "")

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start(*port)
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigChan:
		log.Printf("Browser harness shutting down on signal: %s", sig)
	case err := <-errCh:
		log.Fatalf("browser harness server error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := server.Stop(ctx); err != nil {
		log.Printf("browser harness stop warning: %v", err)
	}
	registry.StopAll()
	bluetoothManager.DisconnectBluetooth()
}
