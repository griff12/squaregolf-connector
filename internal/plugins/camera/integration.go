package camera

import (
	"context"
	"log"
	"sync"

	"github.com/brentyates/squaregolf-connector/internal/core/protocol"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
)

// Manager is the camera plugin: vendor-neutral orchestration (enable/disable,
// shot buffering, status reporting) that delegates the actual camera API calls
// to a Vendor. It depends only on the plugin host contract and the pure protocol
// types — never on the engine. The most isolated outsider: it consumes events
// and reports status, asking nothing of the launch monitor.
type Manager struct {
	host               plugin.Host
	vendor             Vendor
	baseURL            string
	enabled            bool
	pendingFilename    string                // Stores filename from shot-detected to update with club metrics later
	pendingClubMetrics *protocol.ClubMetrics // Buffers club metrics that arrive before shot-detected response
	mu                 sync.Mutex
}

// New builds the camera plugin backed by a specific vendor. Use NewSwingCamVendor
// for the default camera system, or any other Vendor implementation.
func New(vendor Vendor, baseURL string, enabled bool) *Manager {
	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}
	return &Manager{
		vendor:  vendor,
		baseURL: baseURL,
		enabled: enabled,
	}
}

func (m *Manager) Name() string { return "camera" }

// Start subscribes to the device events the camera reacts to. Subscriptions are
// registered once; the enabled flag gates the callback bodies at runtime.
func (m *Manager) Start(ctx context.Context, host plugin.Host) error {
	m.host = host
	host.OnBallReady(m.onBallReadyChanged)
	host.OnBallMetrics(m.onLastBallMetricsChanged)
	host.OnClubMetrics(m.onLastClubMetricsChanged)
	if m.enabled {
		log.Printf("Camera integration initialized with URL: %s", m.baseURL)
	} else {
		log.Println("Camera integration initialized but disabled")
	}
	return nil
}

func (m *Manager) Stop() error {
	m.SetEnabled(false)
	return nil
}

// recordSuccess marks the most recent camera call as healthy.
func (m *Manager) recordSuccess() {
	if m.host != nil {
		m.host.ReportStatus(m.Name(), plugin.StatusConnected, nil)
	}
}

// recordError surfaces a camera failure to the host instead of swallowing it.
func (m *Manager) recordError(err error) {
	log.Printf("Camera error: %v", err)
	if m.host != nil {
		m.host.ReportStatus(m.Name(), plugin.StatusError, err)
	}
}

// IsEnabled returns whether the camera integration is enabled
func (m *Manager) IsEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}

// SetEnabled enables or disables the camera integration
func (m *Manager) SetEnabled(enabled bool) {
	m.mu.Lock()
	wasEnabled := m.enabled
	m.enabled = enabled
	m.mu.Unlock()

	if wasEnabled == enabled {
		return
	}
	if enabled {
		log.Println("Camera integration enabled")
	} else {
		log.Println("Camera integration disabled")
	}
}

// SetBaseURL updates the camera base URL
func (m *Manager) SetBaseURL(baseURL string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if baseURL == "" {
		baseURL = "http://localhost:5000"
	}

	m.baseURL = baseURL
	m.vendor.SetBaseURL(baseURL)
	log.Printf("Camera base URL updated to: %s", baseURL)
}

// GetBaseURL returns the current camera base URL
func (m *Manager) GetBaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.baseURL
}

// Arm tells the camera to start recording (fire and forget).
func (m *Manager) Arm() error {
	m.mu.Lock()
	enabled := m.enabled
	// Clear any pending state from a previous shot.
	m.pendingFilename = ""
	m.pendingClubMetrics = nil
	m.mu.Unlock()

	if !enabled {
		log.Println("Camera integration disabled, skipping arm command")
		return nil
	}

	if err := m.vendor.Arm(); err != nil {
		m.recordError(err)
		return nil
	}

	m.recordSuccess()
	log.Println("Camera arm command sent successfully")
	return nil
}

// ShotDetected saves the clip with ball metrics. Club metrics follow separately
// via UpdateMetadata when they arrive.
func (m *Manager) ShotDetected(ballMetrics *protocol.BallMetrics) error {
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()

	if !enabled {
		log.Println("Camera integration disabled, skipping shot-detected command")
		return nil
	}

	filename, err := m.vendor.ShotDetected(ballMetrics)
	if err != nil {
		m.recordError(err)
		return nil
	}

	m.recordSuccess()

	if filename != "" {
		m.mu.Lock()
		m.pendingFilename = filename
		bufferedClubMetrics := m.pendingClubMetrics
		m.pendingClubMetrics = nil
		m.mu.Unlock()

		log.Printf("Camera shot-detected successful, filename: %s", filename)

		// If club metrics arrived before the filename (race condition), send them now.
		if bufferedClubMetrics != nil {
			log.Printf("Applying buffered club metrics to %s", filename)
			safeGo("update-metadata", func() { m.UpdateMetadata(filename, bufferedClubMetrics) })
		}
	}

	return nil
}

// Cancel aborts an in-progress recording (fire and forget).
func (m *Manager) Cancel() error {
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()

	if !enabled {
		log.Println("Camera integration disabled, skipping cancel command")
		return nil
	}

	if err := m.vendor.Cancel(); err != nil {
		m.recordError(err)
		return nil
	}

	m.recordSuccess()
	log.Println("Camera cancel command sent successfully")
	return nil
}

// UpdateMetadata attaches club metrics to a previously saved clip (fire and forget).
func (m *Manager) UpdateMetadata(filename string, clubMetrics *protocol.ClubMetrics) error {
	m.mu.Lock()
	enabled := m.enabled
	m.mu.Unlock()

	if !enabled {
		log.Println("Camera integration disabled, skipping metadata update")
		return nil
	}

	if filename == "" || clubMetrics == nil {
		return nil
	}

	clubName := ""
	if m.host != nil {
		clubName = m.host.ClubName()
	}

	if err := m.vendor.UpdateMetadata(filename, clubMetrics, clubName); err != nil {
		m.recordError(err)
		return nil
	}

	m.recordSuccess()
	log.Printf("Camera metadata updated successfully for %s with club data", filename)
	return nil
}
