package camera

import (
	"log"
	"sync"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

var (
	cameraInstance *Manager
	cameraOnce     sync.Once
)

// Manager owns the vendor-neutral camera orchestration: enable/disable, shot
// buffering, and surfacing call outcomes into app state. The actual camera API
// calls are delegated to a Vendor.
type Manager struct {
	stateManager       *core.StateManager
	vendor             Vendor
	baseURL            string
	enabled            bool
	pendingFilename    string            // Stores filename from shot-detected to update with club metrics later
	pendingClubMetrics *core.ClubMetrics // Buffers club metrics that arrive before shot-detected response
	mu                 sync.Mutex
}

// GetInstance returns the singleton instance of the camera Manager, backed by
// the default SwingCam vendor.
func GetInstance(stateManager *core.StateManager, baseURL string, enabled bool) *Manager {
	cameraOnce.Do(func() {
		if baseURL == "" {
			baseURL = "http://localhost:5000"
		}

		cameraInstance = &Manager{
			stateManager: stateManager,
			vendor:       NewSwingCamVendor(baseURL),
			baseURL:      baseURL,
			enabled:      enabled,
		}

		// Register state listeners if enabled
		if enabled {
			cameraInstance.registerStateListeners()
			log.Printf("Camera integration initialized with URL: %s", baseURL)
		} else {
			log.Println("Camera integration initialized but disabled")
		}
	})
	return cameraInstance
}

// recordSuccess marks the most recent camera call as healthy.
func (m *Manager) recordSuccess() {
	m.stateManager.SetCameraError(nil)
	m.stateManager.SetCameraStatus(core.CameraStatusOK)
}

// recordError surfaces a camera failure into application state instead of
// swallowing it, so the UI can show the integration is unhealthy.
func (m *Manager) recordError(err error) {
	log.Printf("Camera error: %v", err)
	m.stateManager.SetCameraError(err)
	m.stateManager.SetCameraStatus(core.CameraStatusError)
}

// NewManager builds a camera Manager backed by a specific vendor. Use it for
// non-default camera systems and in tests; GetInstance wires the default
// SwingCam vendor as a singleton.
func NewManager(stateManager *core.StateManager, vendor Vendor, enabled bool) *Manager {
	return &Manager{
		stateManager: stateManager,
		vendor:       vendor,
		enabled:      enabled,
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

	// Update state manager
	m.stateManager.SetCameraEnabled(enabled)

	if enabled {
		// Register state listeners when enabling
		m.registerStateListeners()
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
	m.stateManager.SetCameraURL(&baseURL)
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
func (m *Manager) ShotDetected(ballMetrics *core.BallMetrics) error {
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
func (m *Manager) UpdateMetadata(filename string, clubMetrics *core.ClubMetrics) error {
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
	if name := m.stateManager.GetClubName(); name != nil {
		clubName = *name
	}

	if err := m.vendor.UpdateMetadata(filename, clubMetrics, clubName); err != nil {
		m.recordError(err)
		return nil
	}

	m.recordSuccess()
	log.Printf("Camera metadata updated successfully for %s with club data", filename)
	return nil
}
