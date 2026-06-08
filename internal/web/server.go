package web

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/config"
	"github.com/brentyates/squaregolf-connector/internal/core"
	"github.com/brentyates/squaregolf-connector/internal/plugin"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

type Server struct {
	stateManager         *core.StateManager
	bluetoothManager     *core.BluetoothManager
	launchMonitor        *core.LaunchMonitor
	plugins              *plugin.Registry
	enableExternalCamera bool
	upgrader             websocket.Upgrader
	clients              map[*websocket.Conn]*wsClient
	clientsMu            sync.Mutex
	broadcast            chan []byte
	httpServer           *http.Server
	httpServerMu         sync.Mutex
	webRoot              string
}

// wsClient is a single connected websocket peer. Its send channel is written by
// the broadcaster and drained by a single per-connection writer goroutine. The
// channel is never closed; the owning connection signals teardown by closing
// done exactly once, so sends can never race with a close.
type wsClient struct {
	send chan []byte
	done chan struct{}
}

type WSMessage struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type DeviceStatus struct {
	ConnectionStatus    string                   `json:"connectionStatus"`
	DeviceName          *string                  `json:"deviceName"`
	BatteryLevel        *int                     `json:"batteryLevel"`
	FirmwareVersion     *string                  `json:"firmwareVersion"`
	LauncherVersion     *string                  `json:"launcherVersion"`
	MMIVersion          *string                  `json:"mmiVersion"`
	LaunchMonitorStatus core.LaunchMonitorStatus `json:"launchMonitorStatus"`
	BallDetected        bool                     `json:"ballDetected"`
	BallReady           bool                     `json:"ballReady"`
	BallPosition        *core.BallPosition       `json:"ballPosition"`
	Club                *core.ClubType           `json:"club"`
	Handedness          *core.HandednessType     `json:"handedness"`
	LastError           string                   `json:"lastError"`
	LastBallMetrics     *core.BallMetrics        `json:"lastBallMetrics"`
	LastClubMetrics     *core.ClubMetrics        `json:"lastClubMetrics"`
	IsAligning          bool                     `json:"isAligning"`
	AlignmentAngle      float64                  `json:"alignmentAngle"`
	IsAligned           bool                     `json:"isAligned"`
	DeviceType          core.DeviceType          `json:"deviceType"`
	OmniHomeGolfStatus  *int                     `json:"omniHomeGolfStatus"`
	OmniStatus          *int                     `json:"omniStatus"`
	OmniClubSelection   *int                     `json:"omniClubSelection"`
	OmniSensorStatus    *int                     `json:"omniSensorStatus"`
	CapacitorReady      bool                     `json:"capacitorReady"`
	BatteryCharging     *int                     `json:"batteryCharging"`
}

type GSProStatus struct {
	ConnectionStatus string `json:"connectionStatus"`
	IP               string `json:"ip"`
	Port             int    `json:"port"`
	AutoConnect      bool   `json:"autoConnect"`
	LastError        string `json:"lastError"`
}

type InfiniteTeesStatus struct {
	ConnectionStatus string `json:"connectionStatus"`
	IP               string `json:"ip"`
	Port             int    `json:"port"`
	AutoConnect      bool   `json:"autoConnect"`
	LastError        string `json:"lastError"`
}

type CameraConfig struct {
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

type AppSettings struct {
	DeviceName              string `json:"deviceName"`
	SpinMode                string `json:"spinMode"`
	OmniSpeedUnit           string `json:"omniSpeedUnit"`
	OmniDistanceUnit        string `json:"omniDistanceUnit"`
	OmniGreenSpeed          int    `json:"omniGreenSpeed"`
	OmniCarryAdjustment     int    `json:"omniCarryAdjustment"`
	GSProIP                 string `json:"gsproIP"`
	GSProPort               int    `json:"gsproPort"`
	GSProAutoConnect        bool   `json:"gsproAutoConnect"`
	InfiniteTeesIP          string `json:"infiniteTeesIP"`
	InfiniteTeesPort        int    `json:"infiniteTeesPort"`
	InfiniteTeesAutoConnect bool   `json:"infiniteTeesAutoConnect"`
}

type FeatureFlags struct {
	ExternalCamera bool `json:"externalCamera"`
}

// NewServer builds the HTTP/WebSocket server over the already-assembled plugin
// registry. It does not import or construct any concrete plugin — those are
// wired solely in the composition root (main).
func NewServer(stateManager *core.StateManager, bluetoothManager *core.BluetoothManager, launchMonitor *core.LaunchMonitor, plugins *plugin.Registry, enableExternalCamera bool) *Server {
	server := &Server{
		stateManager:         stateManager,
		bluetoothManager:     bluetoothManager,
		launchMonitor:        launchMonitor,
		plugins:              plugins,
		enableExternalCamera: enableExternalCamera,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				return true
			},
		},
		clients:   make(map[*websocket.Conn]*wsClient),
		broadcast: make(chan []byte, 100),
		webRoot:   resolveWebRoot(),
	}

	server.setupCallbacks()
	go server.handleMessages()

	return server
}

func resolveWebRoot() string {
	candidates := []string{}

	if exePath, err := os.Executable(); err == nil {
		exeDir := filepath.Dir(exePath)
		candidates = append(candidates,
			filepath.Join(exeDir, "..", "Resources", "web"),
			filepath.Join(exeDir, "web"),
		)
	}

	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "web"))
	}

	_, currentFile, _, ok := runtime.Caller(0)
	if ok {
		repoRoot := filepath.Join(filepath.Dir(currentFile), "..", "..")
		candidates = append(candidates, filepath.Join(repoRoot, "web"))
	}

	for _, candidate := range candidates {
		indexPath := filepath.Join(candidate, "index.html")
		if _, err := os.Stat(indexPath); err == nil {
			return candidate
		}
	}

	return "web"
}

func (s *Server) setupCallbacks() {
	// Register all state callbacks to broadcast updates via WebSocket
	s.stateManager.RegisterConnectionStatusCallback(func(oldValue, newValue core.ConnectionStatus) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterDeviceDisplayNameCallback(func(oldValue, newValue *string) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterBatteryLevelCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterLaunchMonitorStatusCallback(func(oldValue, newValue core.LaunchMonitorStatus) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterBallDetectedCallback(func(oldValue, newValue bool) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterBallReadyCallback(func(oldValue, newValue bool) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterBallPositionCallback(func(oldValue, newValue *core.BallPosition) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterClubCallback(func(oldValue, newValue *core.ClubType) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterHandednessCallback(func(oldValue, newValue *core.HandednessType) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterLastErrorCallback(func(oldValue, newValue error) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterLastBallMetricsCallback(func(oldValue, newValue *core.BallMetrics) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterLastClubMetricsCallback(func(oldValue, newValue *core.ClubMetrics) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterDeviceTypeCallback(func(oldValue, newValue core.DeviceType) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterOmniHomeGolfStatusCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterOmniStatusCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterOmniClubSelectionCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterOmniSensorStatusCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterCapacitorReadyCallback(func(oldValue, newValue bool) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterBatteryChargingCallback(func(oldValue, newValue *int) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterGSProStatusCallback(func(oldValue, newValue core.GSProConnectionStatus) {
		s.broadcastGSProStatus()
	})

	s.stateManager.RegisterInfiniteTeesStatusCallback(func(oldValue, newValue core.InfiniteTeesConnectionStatus) {
		s.broadcastInfiniteTeesStatus()
	})

	s.stateManager.RegisterCameraURLCallback(func(oldValue, newValue *string) {
		s.broadcastCameraConfig()
	})

	s.stateManager.RegisterCameraEnabledCallback(func(oldValue, newValue bool) {
		s.broadcastCameraConfig()
	})

	s.stateManager.RegisterCameraStatusCallback(func(oldValue, newValue core.CameraConnectionStatus) {
		s.broadcastCameraConfig()
	})

	s.stateManager.RegisterIsAligningCallback(func(oldValue, newValue bool) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterAlignmentAngleCallback(func(oldValue, newValue float64) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterIsAlignedCallback(func(oldValue, newValue bool) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterFirmwareVersionCallback(func(oldValue, newValue *string) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterLauncherVersionCallback(func(oldValue, newValue *string) {
		s.broadcastDeviceStatus()
	})

	s.stateManager.RegisterMMIVersionCallback(func(oldValue, newValue *string) {
		s.broadcastDeviceStatus()
	})
}

func (s *Server) handleMessages() {
	for message := range s.broadcast {
		s.clientsMu.Lock()
		activeClients := make([]*wsClient, 0, len(s.clients))
		for _, client := range s.clients {
			activeClients = append(activeClients, client)
		}
		s.clientsMu.Unlock()

		for _, client := range activeClients {
			select {
			case client.send <- message:
			case <-client.done:
			default:
				log.Printf("WebSocket client buffer full, dropping message")
			}
		}
	}
}

func (s *Server) broadcastDeviceStatus() {
	status := s.getDeviceStatus()
	log.Printf("Broadcasting device status - BallDetected: %v, BallPosition: %+v", status.BallDetected, status.BallPosition)
	msg := WSMessage{Type: "deviceStatus", Data: status}
	data, _ := json.Marshal(msg)
	select {
	case s.broadcast <- data:
	default:
	}
}

func (s *Server) broadcastGSProStatus() {
	status := s.getGSProStatus()
	msg := WSMessage{Type: "gsproStatus", Data: status}
	data, _ := json.Marshal(msg)
	select {
	case s.broadcast <- data:
	default:
	}
}

func (s *Server) broadcastInfiniteTeesStatus() {
	status := s.getInfiniteTeesStatus()
	msg := WSMessage{Type: "infiniteTeesStatus", Data: status}
	data, _ := json.Marshal(msg)
	select {
	case s.broadcast <- data:
	default:
	}
}

func (s *Server) getDeviceStatus() DeviceStatus {
	var lastErrorStr string
	if err := s.stateManager.GetLastError(); err != nil {
		lastErrorStr = err.Error()
	}

	connectionStatus := "disconnected"
	switch s.stateManager.GetConnectionStatus() {
	case core.ConnectionStatusConnected:
		connectionStatus = "connected"
	case core.ConnectionStatusScanning:
		connectionStatus = "scanning"
	case core.ConnectionStatusConnecting:
		connectionStatus = "connecting"
	case core.ConnectionStatusError:
		connectionStatus = "error"
	}

	return DeviceStatus{
		ConnectionStatus:    connectionStatus,
		DeviceName:          s.stateManager.GetDeviceDisplayName(),
		BatteryLevel:        s.stateManager.GetBatteryLevel(),
		FirmwareVersion:     s.stateManager.GetFirmwareVersion(),
		LauncherVersion:     s.stateManager.GetLauncherVersion(),
		MMIVersion:          s.stateManager.GetMMIVersion(),
		LaunchMonitorStatus: s.stateManager.GetLaunchMonitorStatus(),
		BallDetected:        s.stateManager.GetBallDetected(),
		BallReady:           s.stateManager.GetBallReady(),
		BallPosition:        s.stateManager.GetBallPosition(),
		Club:                s.stateManager.GetClub(),
		Handedness:          s.stateManager.GetHandedness(),
		LastError:           lastErrorStr,
		LastBallMetrics:     s.stateManager.GetLastBallMetrics(),
		LastClubMetrics:     s.stateManager.GetLastClubMetrics(),
		IsAligning:          s.stateManager.GetIsAligning(),
		AlignmentAngle:      s.stateManager.GetAlignmentAngle(),
		IsAligned:           s.stateManager.GetIsAligned(),
		DeviceType:          s.stateManager.GetDeviceType(),
		OmniHomeGolfStatus:  s.stateManager.GetOmniHomeGolfStatus(),
		OmniStatus:          s.stateManager.GetOmniStatus(),
		OmniClubSelection:   s.stateManager.GetOmniClubSelection(),
		OmniSensorStatus:    s.stateManager.GetOmniSensorStatus(),
		CapacitorReady:      s.stateManager.GetCapacitorReady(),
		BatteryCharging:     s.stateManager.GetBatteryCharging(),
	}
}

func (s *Server) getGSProStatus() GSProStatus {
	var lastErrorStr string
	if err := s.stateManager.GetGSProError(); err != nil {
		lastErrorStr = err.Error()
	}

	connectionStatus := "disconnected"
	switch s.stateManager.GetGSProStatus() {
	case core.GSProStatusConnected:
		connectionStatus = "connected"
	case core.GSProStatusConnecting:
		connectionStatus = "connecting"
	case core.GSProStatusError:
		connectionStatus = "error"
	}

	// Get current GSPro settings from integration and config
	ip, port := s.connectionInfo("gspro")
	settings := config.GetInstance().GetSettings()

	return GSProStatus{
		ConnectionStatus: connectionStatus,
		IP:               ip,
		Port:             port,
		AutoConnect:      settings.GSProAutoConnect,
		LastError:        lastErrorStr,
	}
}

func (s *Server) getInfiniteTeesStatus() InfiniteTeesStatus {
	var lastErrorStr string
	if err := s.stateManager.GetInfiniteTeesError(); err != nil {
		lastErrorStr = err.Error()
	}

	connectionStatus := "disconnected"
	switch s.stateManager.GetInfiniteTeesStatus() {
	case core.InfiniteTeesStatusConnected:
		connectionStatus = "connected"
	case core.InfiniteTeesStatusConnecting:
		connectionStatus = "connecting"
	case core.InfiniteTeesStatusError:
		connectionStatus = "error"
	}

	ip, port := s.connectionInfo("infinitetees")
	settings := config.GetInstance().GetSettings()

	return InfiniteTeesStatus{
		ConnectionStatus: connectionStatus,
		IP:               ip,
		Port:             port,
		AutoConnect:      settings.InfiniteTeesAutoConnect,
		LastError:        lastErrorStr,
	}
}

func (s *Server) Start(port int) error {
	router := mux.NewRouter()

	// Serve static files with no-cache headers for development
	staticHandler := http.StripPrefix("/static/", http.FileServer(http.Dir(filepath.Join(s.webRoot, "static"))))
	router.PathPrefix("/static/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		w.Header().Set("Expires", "0")
		staticHandler.ServeHTTP(w, r)
	}))

	// API routes
	api := router.PathPrefix("/api").Subrouter()

	// Device endpoints
	api.HandleFunc("/device/status", s.handleDeviceStatus).Methods("GET")
	api.HandleFunc("/device/connect", s.handleDeviceConnect).Methods("POST")
	api.HandleFunc("/device/disconnect", s.handleDeviceDisconnect).Methods("POST")
	api.HandleFunc("/device/practice", s.handlePracticeMode).Methods("POST")

	// GSPro endpoints
	api.HandleFunc("/gspro/status", s.handleGSProStatus).Methods("GET")
	api.HandleFunc("/gspro/connect", s.handleGSProConnect).Methods("POST")
	api.HandleFunc("/gspro/disconnect", s.handleGSProDisconnect).Methods("POST")
	api.HandleFunc("/gspro/config", s.handleGSProConfig).Methods("GET", "POST")

	// Infinite Tees endpoints
	api.HandleFunc("/infinitetees/status", s.handleInfiniteTeesStatus).Methods("GET")
	api.HandleFunc("/infinitetees/connect", s.handleInfiniteTeesConnect).Methods("POST")
	api.HandleFunc("/infinitetees/disconnect", s.handleInfiniteTeesDisconnect).Methods("POST")
	api.HandleFunc("/infinitetees/config", s.handleInfiniteTeesConfig).Methods("GET", "POST")

	// Camera endpoints
	api.HandleFunc("/camera/config", s.handleCameraConfig).Methods("GET", "POST")

	// Settings endpoints
	api.HandleFunc("/settings", s.handleSettings).Methods("GET", "POST")

	// Feature flags endpoint
	api.HandleFunc("/features", s.handleFeatures).Methods("GET")

	// Alignment endpoints
	api.HandleFunc("/alignment/start", s.handleAlignmentStart).Methods("POST")
	api.HandleFunc("/alignment/stop", s.handleAlignmentStop).Methods("POST")
	api.HandleFunc("/alignment/cancel", s.handleAlignmentCancel).Methods("POST")
	api.HandleFunc("/alignment/handedness", s.handleAlignmentHandedness).Methods("POST")

	// WebSocket endpoint
	router.HandleFunc("/ws", s.handleWebSocket)

	// Serve index.html for all non-API routes (SPA support)
	router.PathPrefix("/").HandlerFunc(s.handleIndex)

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: router,
	}

	s.httpServerMu.Lock()
	s.httpServer = httpServer
	s.httpServerMu.Unlock()

	log.Printf("Web server starting on port %d", port)
	log.Printf("Access via: http://localhost:%d", port)
	err := httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *Server) Stop(ctx context.Context) error {
	s.httpServerMu.Lock()
	httpServer := s.httpServer
	s.httpServerMu.Unlock()

	if httpServer == nil {
		return nil
	}

	return httpServer.Shutdown(ctx)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	indexPath := filepath.Join(s.webRoot, "index.html")
	http.ServeFile(w, r, indexPath)
}

func (s *Server) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}

	client := &wsClient{
		send: make(chan []byte, 100),
		done: make(chan struct{}),
	}

	s.clientsMu.Lock()
	s.clients[conn] = client
	s.clientsMu.Unlock()

	go func() {
		defer conn.Close()
		for {
			select {
			case msg := <-client.send:
				conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
					log.Printf("WebSocket send error: %v, closing client", err)
					return
				}
			case <-client.done:
				return
			}
		}
	}()

	s.sendInitialStatus(client)

	defer func() {
		s.clientsMu.Lock()
		if _, exists := s.clients[conn]; exists {
			delete(s.clients, conn)
			close(client.done)
		}
		s.clientsMu.Unlock()
		conn.Close()
	}()

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) sendInitialStatus(client *wsClient) {
	send := func(msgType string, payload interface{}) {
		data, _ := json.Marshal(WSMessage{Type: msgType, Data: payload})
		select {
		case client.send <- data:
		case <-client.done:
		}
	}

	send("deviceStatus", s.getDeviceStatus())
	send("gsproStatus", s.getGSProStatus())
	send("infiniteTeesStatus", s.getInfiniteTeesStatus())
	send("cameraConfig", s.getCameraConfig())
}

func (s *Server) handleDeviceStatus(w http.ResponseWriter, r *http.Request) {
	status := s.getDeviceStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleDeviceConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DeviceName string `json:"deviceName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	go s.bluetoothManager.StartBluetoothConnection(req.DeviceName, "")
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeviceDisconnect(w http.ResponseWriter, r *http.Request) {
	go s.bluetoothManager.DisconnectBluetooth()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleGSProStatus(w http.ResponseWriter, r *http.Request) {
	status := s.getGSProStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// connectIntegration connects a registered Connectable plugin by name. The body
// is identical for every integration, so all of them route here.
func (s *Server) connectIntegration(name string, w http.ResponseWriter, r *http.Request) {
	integration, ok := s.plugins.Connectable(name)
	if !ok {
		http.Error(w, "unknown integration", http.StatusNotFound)
		return
	}

	var req struct {
		IP   string `json:"ip"`
		Port int    `json:"port"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	go integration.BeginConnect(req.IP, req.Port)
	w.WriteHeader(http.StatusOK)
}

// disconnectIntegration disconnects a registered Connectable plugin by name.
func (s *Server) disconnectIntegration(name string, w http.ResponseWriter, r *http.Request) {
	integration, ok := s.plugins.Connectable(name)
	if !ok {
		http.Error(w, "unknown integration", http.StatusNotFound)
		return
	}

	go integration.EndConnect()
	w.WriteHeader(http.StatusOK)
}

// connectionInfo returns a Connectable plugin's current host/port, or zero values.
func (s *Server) connectionInfo(name string) (string, int) {
	if c, ok := s.plugins.Connectable(name); ok {
		return c.GetConnectionInfo()
	}
	return "", 0
}

func (s *Server) handleGSProConnect(w http.ResponseWriter, r *http.Request) {
	s.connectIntegration("gspro", w, r)
}

func (s *Server) handleGSProDisconnect(w http.ResponseWriter, r *http.Request) {
	s.disconnectIntegration("gspro", w, r)
}

func (s *Server) handleGSProConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		settings := config.GetInstance().GetSettings()
		configData := struct {
			IP          string `json:"ip"`
			Port        int    `json:"port"`
			AutoConnect bool   `json:"autoConnect"`
		}{
			IP:          settings.GSProIP,
			Port:        settings.GSProPort,
			AutoConnect: settings.GSProAutoConnect,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(configData)
	} else {
		var configData struct {
			IP          string `json:"ip"`
			Port        int    `json:"port"`
			AutoConnect bool   `json:"autoConnect"`
		}
		if err := json.NewDecoder(r.Body).Decode(&configData); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		cfg := config.GetInstance()
		cfg.SetGSProIP(configData.IP)
		cfg.SetGSProPort(configData.Port)
		cfg.SetGSProAutoConnect(configData.AutoConnect)

		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleInfiniteTeesStatus(w http.ResponseWriter, r *http.Request) {
	status := s.getInfiniteTeesStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) handleInfiniteTeesConnect(w http.ResponseWriter, r *http.Request) {
	s.connectIntegration("infinitetees", w, r)
}

func (s *Server) handleInfiniteTeesDisconnect(w http.ResponseWriter, r *http.Request) {
	s.disconnectIntegration("infinitetees", w, r)
}

func (s *Server) handleInfiniteTeesConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		settings := config.GetInstance().GetSettings()
		configData := struct {
			IP          string `json:"ip"`
			Port        int    `json:"port"`
			AutoConnect bool   `json:"autoConnect"`
		}{
			IP:          settings.InfiniteTeesIP,
			Port:        settings.InfiniteTeesPort,
			AutoConnect: settings.InfiniteTeesAutoConnect,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(configData)
	} else {
		var configData struct {
			IP          string `json:"ip"`
			Port        int    `json:"port"`
			AutoConnect bool   `json:"autoConnect"`
		}
		if err := json.NewDecoder(r.Body).Decode(&configData); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		cfg := config.GetInstance()
		cfg.SetInfiniteTeesIP(configData.IP)
		cfg.SetInfiniteTeesPort(configData.Port)
		cfg.SetInfiniteTeesAutoConnect(configData.AutoConnect)

		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" {
		settings := config.GetInstance().GetSettings()

		appSettings := AppSettings{
			DeviceName:              settings.DeviceName,
			SpinMode:                settings.SpinMode,
			OmniSpeedUnit:           settings.OmniSpeedUnit,
			OmniDistanceUnit:        settings.OmniDistanceUnit,
			OmniGreenSpeed:          settings.OmniGreenSpeed,
			OmniCarryAdjustment:     settings.OmniCarryAdjustment,
			GSProIP:                 settings.GSProIP,
			GSProPort:               settings.GSProPort,
			GSProAutoConnect:        settings.GSProAutoConnect,
			InfiniteTeesIP:          settings.InfiniteTeesIP,
			InfiniteTeesPort:        settings.InfiniteTeesPort,
			InfiniteTeesAutoConnect: settings.InfiniteTeesAutoConnect,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(appSettings)
	} else {
		var rawSettings map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&rawSettings); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		cfg := config.GetInstance()

		if rawValue, ok := rawSettings["deviceName"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid deviceName", http.StatusBadRequest)
				return
			}
			cfg.SetDeviceName(value)
		}

		if rawValue, ok := rawSettings["spinMode"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid spinMode", http.StatusBadRequest)
				return
			}
			cfg.SetSpinMode(value)

			var spinMode core.SpinMode
			if value == "standard" {
				spinMode = core.Standard
			} else {
				spinMode = core.Advanced
			}
			s.stateManager.SetSpinMode(&spinMode)
		}

		if rawValue, ok := rawSettings["omniSpeedUnit"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid omniSpeedUnit", http.StatusBadRequest)
				return
			}
			if value != "mps" && value != "mph" {
				http.Error(w, "Invalid omniSpeedUnit value", http.StatusBadRequest)
				return
			}
			cfg.SetOmniSpeedUnit(value)
			s.stateManager.SetOmniSpeedUnit(&value)
		}

		if rawValue, ok := rawSettings["omniDistanceUnit"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid omniDistanceUnit", http.StatusBadRequest)
				return
			}
			if value != "meters" && value != "mixed" && value != "yards" {
				http.Error(w, "Invalid omniDistanceUnit value", http.StatusBadRequest)
				return
			}
			cfg.SetOmniDistanceUnit(value)
			s.stateManager.SetOmniDistanceUnit(&value)
		}

		if rawValue, ok := rawSettings["omniGreenSpeed"]; ok {
			var value int
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid omniGreenSpeed", http.StatusBadRequest)
				return
			}
			if value < 8 || value > 13 {
				http.Error(w, "Invalid omniGreenSpeed value", http.StatusBadRequest)
				return
			}
			cfg.SetOmniGreenSpeed(value)
			s.stateManager.SetOmniGreenSpeed(&value)
		}

		if rawValue, ok := rawSettings["omniCarryAdjustment"]; ok {
			var value int
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid omniCarryAdjustment", http.StatusBadRequest)
				return
			}
			if value < -99 || value > 99 {
				http.Error(w, "Invalid omniCarryAdjustment value", http.StatusBadRequest)
				return
			}
			cfg.SetOmniCarryAdjustment(value)
			s.stateManager.SetOmniCarryAdjustment(&value)
		}

		if rawValue, ok := rawSettings["gsproIP"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid gsproIP", http.StatusBadRequest)
				return
			}
			cfg.SetGSProIP(value)
		}

		if rawValue, ok := rawSettings["gsproPort"]; ok {
			var value int
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid gsproPort", http.StatusBadRequest)
				return
			}
			cfg.SetGSProPort(value)
		}

		if rawValue, ok := rawSettings["gsproAutoConnect"]; ok {
			var value bool
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid gsproAutoConnect", http.StatusBadRequest)
				return
			}
			cfg.SetGSProAutoConnect(value)
		}

		if rawValue, ok := rawSettings["infiniteTeesIP"]; ok {
			var value string
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid infiniteTeesIP", http.StatusBadRequest)
				return
			}
			cfg.SetInfiniteTeesIP(value)
		}

		if rawValue, ok := rawSettings["infiniteTeesPort"]; ok {
			var value int
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid infiniteTeesPort", http.StatusBadRequest)
				return
			}
			cfg.SetInfiniteTeesPort(value)
		}

		if rawValue, ok := rawSettings["infiniteTeesAutoConnect"]; ok {
			var value bool
			if err := json.Unmarshal(rawValue, &value); err != nil {
				http.Error(w, "Invalid infiniteTeesAutoConnect", http.StatusBadRequest)
				return
			}
			cfg.SetInfiniteTeesAutoConnect(value)
		}

		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) getCameraConfig() CameraConfig {
	url := "http://localhost:5000"
	if cameraURL := s.stateManager.GetCameraURL(); cameraURL != nil {
		url = *cameraURL
	}

	enabled := s.stateManager.GetCameraEnabled()

	errStr := ""
	if cameraErr := s.stateManager.GetCameraError(); cameraErr != nil {
		errStr = cameraErr.Error()
	}

	return CameraConfig{
		URL:     url,
		Enabled: enabled,
		Status:  string(s.stateManager.GetCameraStatus()),
		Error:   errStr,
	}
}

func (s *Server) handleCameraConfig(w http.ResponseWriter, r *http.Request) {
	// Return 404 if external camera feature is disabled
	if !s.enableExternalCamera {
		http.Error(w, "External camera feature not enabled", http.StatusNotFound)
		return
	}

	if r.Method == "GET" {
		cameraConfig := s.getCameraConfig()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(cameraConfig)
	} else {
		var cameraConfig CameraConfig
		if err := json.NewDecoder(r.Body).Decode(&cameraConfig); err != nil {
			http.Error(w, "Invalid request body", http.StatusBadRequest)
			return
		}

		// Save camera settings to config
		cfg := config.GetInstance()
		cfg.SetCameraURL(cameraConfig.URL)
		cfg.SetCameraEnabled(cameraConfig.Enabled)

		// Update camera URL and enabled state in state manager
		s.stateManager.SetCameraURL(&cameraConfig.URL)
		s.stateManager.SetCameraEnabled(cameraConfig.Enabled)

		// Update the camera plugin (if registered)
		if cam, ok := s.plugins.Configurable("camera"); ok {
			cam.SetBaseURL(cameraConfig.URL)
			cam.SetEnabled(cameraConfig.Enabled)
		}

		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) broadcastCameraConfig() {
	config := s.getCameraConfig()
	msg := WSMessage{Type: "cameraConfig", Data: config}
	data, _ := json.Marshal(msg)
	select {
	case s.broadcast <- data:
	default:
	}
}

func (s *Server) handleFeatures(w http.ResponseWriter, r *http.Request) {
	features := FeatureFlags{
		ExternalCamera: s.enableExternalCamera,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(features)
}

func (s *Server) handleAlignmentStart(w http.ResponseWriter, r *http.Request) {
	err := s.launchMonitor.StartAlignment()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAlignmentStop(w http.ResponseWriter, r *http.Request) {
	err := s.launchMonitor.StopAlignment()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAlignmentCancel(w http.ResponseWriter, r *http.Request) {
	err := s.launchMonitor.CancelAlignment()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleAlignmentHandedness(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Handedness string `json:"handedness"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	// Convert string to HandednessType
	var handedness core.HandednessType
	if req.Handedness == "left" {
		handedness = core.LeftHanded
	} else if req.Handedness == "right" {
		handedness = core.RightHanded
	} else {
		http.Error(w, "Invalid handedness value (must be 'left' or 'right')", http.StatusBadRequest)
		return
	}

	// Update state manager
	s.stateManager.SetHandedness(&handedness)

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handlePracticeMode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	var err error
	if req.Enabled {
		err = s.launchMonitor.ActivateBallDetection()
	} else {
		err = s.launchMonitor.DeactivateBallDetection()
	}

	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
