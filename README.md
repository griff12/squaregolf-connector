# SquareGolf Connector

An unofficial launch monitor connector for SquareGolf devices with Open API (GSPro Connect) integration.

## Download And Run (macOS)

1. Open the [latest release](https://github.com/brentyates/squaregolf-connector/releases/latest).
2. Download the macOS zip file: `SquareGolf-Connector-<version>-macOS.zip`.
3. Unzip it.
4. Drag `SquareGolf Connector.app` into `Applications` if you want.
5. Open `SquareGolf Connector.app`.

When the app is running, it opens its own desktop window. Closing the window shuts the app down.

## Download And Run (Windows)

1. Open the [latest release](https://github.com/brentyates/squaregolf-connector/releases/latest).
2. Download the Windows zip file: `SquareGolf-Connector-<version>-Windows.zip`.
3. Unzip it. You will get a `SquareGolf Connector` folder with the app executable and a `web` folder next to it. Keep them together.
4. Open the folder and double-click `SquareGolf Connector.exe`.

Windows 10 or 11 is required. The app uses the built-in Microsoft Edge WebView2 runtime, which ships with recent versions of Windows. If the window does not open, install the [Microsoft Edge WebView2 Runtime](https://developer.microsoft.com/en-us/microsoft-edge/webview2/).

The first time you launch, Windows SmartScreen may warn that the app is from an unidentified developer. Click `More info`, then `Run anyway`.

## First Launch On macOS

Because this app is not signed with a paid Apple Developer ID, macOS may block it the first time you open it.

You may see a message like:

- `"SquareGolf Connector.app" can't be opened because Apple could not verify it for malware`
- `"SquareGolf Connector.app" is from an unidentified developer`

If that happens, do this:

1. Try to open `SquareGolf Connector.app` once, then close the warning message.
2. Right-click `SquareGolf Connector.app` and choose `Open`.
3. Click `Open` in the confirmation dialog.
4. If macOS still blocks it, open `System Settings`.
5. Go to `Privacy & Security`.
6. Scroll down until you see the message about `SquareGolf Connector`.
7. Click `Open Anyway`.
8. If macOS asks again, click `Open`.

You usually only need to do this once.

## What It Does

SquareGolf Connector connects to SquareGolf Bluetooth launch monitors and provides:

- **Bluetooth connectivity** to SquareGolf devices
- **Open API integration** (GSPro, Infinite Tees, and compatible sims) with automatic reconnection
- **Desktop window UI** powered by the existing web frontend
- **External camera integration** (experimental)
- **Persistent saved settings**

## Features

- Real-time ball and club metrics
- Battery monitoring
- Device alignment tracking
- Ball detection and position tracking
- Configurable club selection and handedness
- Persistent settings storage
- Unified shot timeline for launch data and plugin-contributed metrics, feedback, charts, and media
- Auto-connect functionality

## Requirements

- **macOS** or **Windows 10/11** for the ready-to-download app releases
- Bluetooth adapter
- A SquareGolf launch monitor for normal use

## Plugin Development

Integrations are intentionally lightweight connectors. They subscribe to canonical shots and publish structured results back into the shared shot timeline, allowing multiple devices such as swing cameras and wrist sensors to contribute to the same experience. See [Plugin Architecture](docs/PLUGIN_ARCHITECTURE.md).

## Quick Start

1. Launch `SquareGolf Connector.app`.
2. Turn on your SquareGolf device.
3. Open your simulator (GSPro, Infinite Tees, ...) if you use one.
4. In the app, connect to your device.
5. If needed, connect your simulator from the Integrations screen (the "Open API" card — set its host/port).

## Troubleshooting

### macOS says the app cannot be opened

1. Try to open the app once and close the warning.
2. Right-click the app and choose `Open`.
3. Click `Open`.
4. If it is still blocked, open `System Settings` -> `Privacy & Security` and click `Open Anyway`.

### Cannot connect to Bluetooth device

- Ensure your Bluetooth adapter is enabled
- Make sure your SquareGolf device is turned on
- Move the device closer to your Mac

### GSPro not receiving data

- Make sure GSPro is open
- Check the connection settings in the app
- Enable auto-reconnect in settings

### App window opens but looks blank or broken

- Quit the app and open it again
- Download the latest release from GitHub
- If the problem continues, open an issue on GitHub

## License

See [LICENSE](LICENSE) file for details.

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## Disclaimer

This is an unofficial, community-developed connector and is not affiliated with or endorsed by SquareGolf.
