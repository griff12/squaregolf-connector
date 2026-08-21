package core

// IntegrationStatus is the generic, per-plugin connection/health state that
// drives the data-driven integrations UI. It replaces the per-integration status
// fields (GSProStatus, InfiniteTeesStatus, ...) for any new consumer.
type IntegrationStatus struct {
	Status string `json:"status"` // disconnected | connecting | connected | error
	Error  string `json:"error,omitempty"`
}

// SetIntegrationStatus records a plugin's status by name and fans out to
// registered observers (panic-isolated, like the typed callbacks).
func (sm *StateManager) SetIntegrationStatus(name string, status IntegrationStatus) {
	sm.mu.Lock()
	if sm.integrationStatuses == nil {
		sm.integrationStatuses = make(map[string]IntegrationStatus)
	}
	sm.integrationStatuses[name] = status
	callbacks := sm.integrationStatusCallbacks
	sm.mu.Unlock()

	for _, callback := range callbacks {
		func() {
			defer func() { recover() }()
			callback(name, status)
		}()
	}
}

// GetIntegrationStatus returns a plugin's last reported status.
func (sm *StateManager) GetIntegrationStatus(name string) IntegrationStatus {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if status, ok := sm.integrationStatuses[name]; ok {
		return status
	}
	return IntegrationStatus{Status: "disconnected"}
}

// RegisterIntegrationStatusCallback subscribes to per-plugin status changes.
func (sm *StateManager) RegisterIntegrationStatusCallback(callback func(name string, status IntegrationStatus)) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.integrationStatusCallbacks = append(sm.integrationStatusCallbacks, callback)
}
