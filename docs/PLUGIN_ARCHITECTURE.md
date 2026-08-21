# Plugin Architecture

SquareGolf Connector is the user-facing home for a golf session. Plugins connect external devices and services to that shared experience; they should not need to recreate shot history, metric presentation, or feedback UI in a separate application.

## Data Flow

1. The launch monitor records a canonical `plugin.Shot` with a stable ID.
2. Every started plugin can subscribe through `Host.OnShot`.
3. A plugin captures or retrieves its device-specific data and interprets it.
4. The plugin calls `Host.PublishResult`, using the shot ID as `CorrelationID`.
5. The app adds the result to that shot and updates both the history API and WebSocket clients.

Callbacks are delivered asynchronously and in order for each subscriber. Plugins must retain and close their subscription from `Stop`.

```go
func (p *Plugin) Start(ctx context.Context, host plugin.Host) error {
	p.subscription = host.OnShot(func(shot plugin.Shot) {
		result, err := p.analyze(ctx, shot)
		if err != nil {
			host.ReportStatus(p.Name(), plugin.StatusError, err)
			return
		}
		result.Plugin = p.Name()
		result.Kind = "wrist.feedback"
		result.CorrelationID = shot.ID
		if err := host.PublishResult(result); err != nil {
			host.ReportStatus(p.Name(), plugin.StatusError, err)
		}
	})
	return nil
}
```

## Result Contract

`plugin.Result` has common, generically rendered fields:

- `Summary` for the result's short overview.
- `Metrics` for scalar measurements, optionally including phase, status, and target range.
- `Insights` for interpretation and actionable recommendations.
- `Series` for chartable samples such as wrist angles through swing phases.
- `Media` for videos and images served by the plugin or another reachable service.
- `Links` for optional handoff to a specialized experience.
- `Data` for versioned plugin-specific JSON that the generic UI does not interpret.

Each result declares a `Kind` and `SchemaVersion`. Common fields should contain everything needed for the unified UI; `Data` preserves richer details for future specialized renderers without coupling the core to a device.

Shot history is currently bounded to the latest 100 shots in memory. The API boundary is designed so persistence can be added without changing plugin implementations.

## Optional Capabilities

Plugins can implement only the capabilities they need:

- `Describable` and `ConfigStore` make the integration appear in the data-driven settings UI.
- `ConfigValidator` checks settings before they are saved.
- `ConnectionLifecycle` provides transport-neutral connect and disconnect operations for Bluetooth, USB, local agents, or network services.
- `Actionable` exposes manifest-declared operations such as scan, pair, calibrate, or test.
- The older `Connectable` interface remains supported for existing host-and-port integrations.

The generic web layer never imports a concrete plugin. Manifests describe configuration and actions; capability interfaces provide behavior.

## Intended Integrations

A HackMotion connector should own device discovery, capture, phase detection, metric calculation, and interpretation. It should publish wrist metrics, time-series curves, and coaching insights to the correlated shot.

A rebuilt Swing Cam connector should own camera discovery, recording, trimming, and media availability. It should publish video media and any derived visual analysis to the same correlated shot.

Neither connector needs its own primary shot-results application. A standalone tool can still exist for calibration, diagnostics, firmware, or advanced workflows, exposed through an optional deep link.
