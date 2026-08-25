package android

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// countingClient records exactly what reaches the wrapped BluetoothClient. Those
// counts ARE the contract: upstream's simulator reads its handler map without a
// lock from two goroutines, so any write after the device is live is a fatal
// "concurrent map read and map write", and any delete re-opens the unguarded
// handler dereference at internal/core/simulator_mock.go:566.
type countingClient struct {
	mu sync.Mutex

	starts []string
	stops  []string
	writes []string

	handlers  map[string]func([]byte)
	connected bool
}

func newCountingClient() *countingClient {
	return &countingClient{handlers: make(map[string]func([]byte)), connected: true}
}

func (m *countingClient) StartNotifications(uuid string, handler func([]byte)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.starts = append(m.starts, uuid)
	m.handlers[uuid] = handler
	return nil
}

func (m *countingClient) StopNotifications(uuid string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stops = append(m.stops, uuid)
	delete(m.handlers, uuid)
	return nil
}

func (m *countingClient) WriteCharacteristic(uuid string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = append(m.writes, uuid)
	return nil
}

// fire mimics the simulator dereferencing its stored handler with no nil check.
func (m *countingClient) fire(uuid string, data []byte) {
	m.mu.Lock()
	h := m.handlers[uuid]
	m.mu.Unlock()
	h(data) // deliberately unguarded, exactly like simulator_mock.go:566
}

func (m *countingClient) Connect(deviceName, deviceAddress string) error { return nil }
func (m *countingClient) Disconnect() error                              { return nil }
func (m *countingClient) ReadCharacteristic(uuid string) ([]byte, error) { return nil, nil }
func (m *countingClient) IsConnected() bool                              { return m.connected }
func (m *countingClient) StartScan(prefix string) error                  { return nil }
func (m *countingClient) StopScan() error                                { return nil }
func (m *countingClient) GetDiscoveredDevices() []string                 { return nil }
func (m *countingClient) GetConnectedDeviceName() string                 { return "" }
func (m *countingClient) GetConnectedDeviceManufacturerData() string     { return "" }

var _ core.BluetoothClient = (*countingClient)(nil)

// TestWriteRefusedBeforeNotificationsArmed is the regression test for the
// connect-side arm window. connectDevice marks the client connected and then
// spends about four seconds on characteristic reads BEFORE calling
// EnableNotifications; the connectapi plugin arms on any unprompted "GSPro ready"
// in that window, and upstream's simulator then dereferences a nil handler in a
// detached goroutine and kills the process.
//
// Remove the armed check from notifyOwner.WriteCharacteristic and this fails.
func TestWriteRefusedBeforeNotificationsArmed(t *testing.T) {
	inner := newCountingClient()
	g := OwnNotifications(inner)

	if err := g.WriteCharacteristic("cmd", []byte{0x11, 0x81}); !errors.Is(err, ErrNotificationsNotArmed) {
		t.Fatalf("write before arming = %v, want ErrNotificationsNotArmed", err)
	}
	inner.mu.Lock()
	n := len(inner.writes)
	inner.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d writes reached the device before notifications were armed", n)
	}

	if err := g.StartNotifications("notify", func([]byte) {}); err != nil {
		t.Fatalf("start notifications: %v", err)
	}
	if err := g.WriteCharacteristic("cmd", []byte{0x11, 0x81}); err != nil {
		t.Fatalf("write after arming: %v", err)
	}
}

// TestHandlerTableIsWrittenAtMostOncePerUUID is the regression test for both
// simulator killers at once. The wrapped client must see exactly one
// StartNotifications per UUID for the life of the process and never a
// StopNotifications, no matter how many connect/disconnect cycles run.
func TestHandlerTableIsWrittenAtMostOncePerUUID(t *testing.T) {
	inner := newCountingClient()
	g := OwnNotifications(inner)

	for cycle := 0; cycle < 3; cycle++ {
		if err := g.StartNotifications("notify", func([]byte) {}); err != nil {
			t.Fatalf("cycle %d start: %v", cycle, err)
		}
		if err := g.StartNotifications("battery", func([]byte) {}); err != nil {
			t.Fatalf("cycle %d start battery: %v", cycle, err)
		}
		// Upstream's teardown calls StopNotifications twice, then Disconnect
		// (internal/core/bluetooth_manager.go:418-430).
		if err := g.StopNotifications("notify"); err != nil {
			t.Fatalf("cycle %d stop: %v", cycle, err)
		}
		if err := g.StopNotifications("battery"); err != nil {
			t.Fatalf("cycle %d stop battery: %v", cycle, err)
		}
		if err := g.Disconnect(); err != nil {
			t.Fatalf("cycle %d disconnect: %v", cycle, err)
		}
	}

	inner.mu.Lock()
	starts := append([]string(nil), inner.starts...)
	stops := append([]string(nil), inner.stops...)
	inner.mu.Unlock()

	if len(starts) != 2 {
		t.Fatalf("wrapped client saw %d StartNotifications (%v), want exactly 2", len(starts), starts)
	}
	if len(stops) != 0 {
		t.Fatalf("wrapped client saw %d StopNotifications (%v), want 0", len(stops), stops)
	}
}

// TestFireAfterStopDoesNotPanic proves the disconnect-side window is closed: a
// detection cycle still running when the app tears notifications down must find a
// live fan-out closure that no-ops, not a nil map entry.
func TestFireAfterStopDoesNotPanic(t *testing.T) {
	inner := newCountingClient()
	g := OwnNotifications(inner)

	delivered := 0
	if err := g.StartNotifications("notify", func([]byte) { delivered++ }); err != nil {
		t.Fatalf("start: %v", err)
	}
	inner.fire("notify", []byte{1})
	if delivered != 1 {
		t.Fatalf("delivered = %d, want 1", delivered)
	}

	if err := g.StopNotifications("notify"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// This is the call that kills the process without the decorator.
	inner.fire("notify", []byte{2})
	if delivered != 1 {
		t.Fatalf("packet delivered after StopNotifications: delivered = %d", delivered)
	}
}

// TestCapabilitySettersForward proves the decorator does not swallow the phase and
// connection-lost callbacks. BluetoothManager asserts against the value it was
// handed -- the decorator -- so without forwarding, a Client behind it would
// silently lose unsolicited-disconnect detection.
func TestCapabilitySettersForward(t *testing.T) {
	c := NewClient(&fakeBridge{})
	defer c.Close()

	g := OwnNotifications(c)
	capable, ok := g.(CallbackCapableClient)
	if !ok {
		t.Fatal("decorator does not satisfy CallbackCapableClient")
	}

	got := make(chan struct{}, 1)
	capable.SetConnectionLostCallback(func() { got <- struct{}{} })

	c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")
	c.OnConnectionStateChanged(ConnStateDisconnected, "AA:BB", "link loss")

	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("connection-lost callback was not forwarded through the decorator")
	}
}
