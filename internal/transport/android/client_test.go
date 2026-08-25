package android

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brentyates/squaregolf-connector/internal/core"
)

// fakeBridge stands in for the Kotlin implementation.
type fakeBridge struct {
	mu sync.Mutex

	onConnect func(f *fakeBridge) error
	connErr   error
	writeErr  error
	readData  []byte

	writes []string
	notify []string
}

func (f *fakeBridge) Connect(deviceName, deviceAddress, namePrefix string) error {
	f.mu.Lock()
	hook := f.onConnect
	err := f.connErr
	f.mu.Unlock()
	if hook != nil {
		return hook(f)
	}
	return err
}

func (f *fakeBridge) Disconnect() error { return nil }

func (f *fakeBridge) StartScan(namePrefix string) error { return nil }

func (f *fakeBridge) StopScan() error { return nil }

func (f *fakeBridge) WriteCharacteristic(uuid string, data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = append(f.writes, uuid)
	return f.writeErr
}

func (f *fakeBridge) ReadCharacteristic(uuid string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.readData, nil
}

func (f *fakeBridge) StartNotifications(uuid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.notify = append(f.notify, uuid)
	return nil
}

func (f *fakeBridge) StopNotifications(uuid string) error { return nil }

// emptyMessageError reproduces a Kotlin Throwable constructed with no message:
// gobind converts getMessage()==null into the empty Go string.
type emptyMessageError struct{}

func (emptyMessageError) Error() string { return "" }

// TestConnectFlushesPhaseEventsBeforeReturning is the regression test for the
// ordering defect that flushLifecycle exists to fix.
//
// BluetoothManager sets ConnectionStatusConnected synchronously the moment
// Connect returns (internal/core/bluetooth_manager.go:402). The phase events
// Kotlin raises DURING the connect travel on the asynchronous lifecycle lane, so
// without the barrier they land AFTER "connected" and the UI is left showing
// "connecting" on a live link.
//
// Delete flushLifecycle from Connect and this test fails.
func TestConnectFlushesPhaseEventsBeforeReturning(t *testing.T) {
	// The first phase callback is deliberately slow. Without the barrier, Connect
	// returns in microseconds and cannot have seen both phases; with it, Connect
	// must wait out the whole lane. The delay is the instrument, not sequencing.
	const slowCallback = 250 * time.Millisecond

	c := NewClient(nil)
	defer c.Close()

	var mu sync.Mutex
	var phases []core.ConnectionPhase
	c.SetPhaseChangeCallback(func(p core.ConnectionPhase) {
		if p == core.PhaseScanning {
			time.Sleep(slowCallback)
		}
		mu.Lock()
		phases = append(phases, p)
		mu.Unlock()
	})

	bridge := &fakeBridge{}
	bridge.onConnect = func(f *fakeBridge) error {
		// Kotlin reports progress from its own threads while Go is parked here.
		c.OnConnectionStateChanged(ConnStateScanning, "", "")
		c.OnConnectionStateChanged(ConnStateConnecting, "AA:BB", "")
		c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")
		return nil
	}
	c.bridge = bridge

	start := time.Now()
	if err := c.Connect("SquareGolf(1234)", ""); err != nil {
		t.Fatalf("connect: %v", err)
	}
	elapsed := time.Since(start)

	// The assertion is made immediately on return, with no sleep: that is the
	// whole point. Both phases must already have been delivered.
	mu.Lock()
	got := append([]core.ConnectionPhase(nil), phases...)
	mu.Unlock()

	if len(got) != 2 || got[0] != core.PhaseScanning || got[1] != core.PhaseConnecting {
		t.Fatalf("phase events not drained before Connect returned after %v: got %v, want [scanning connecting]", elapsed, got)
	}
	if elapsed < slowCallback {
		t.Fatalf("Connect returned in %v without waiting for the lifecycle lane", elapsed)
	}
}

// TestConnectDoesNotResurrectADeadLink covers the lost-wakeup that an
// unconditional connected.Store(true) after Bridge.Connect would introduce: the
// link dies while Go is inside Kotlin, the disconnect callback fires, and the
// returning Connect would otherwise mark the client connected forever. Android
// reports onConnectionStateChange(DISCONNECTED) once, so nothing would correct it.
func TestConnectDoesNotResurrectADeadLink(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()

	var lost sync.WaitGroup
	lost.Add(1)
	var once sync.Once
	c.SetConnectionLostCallback(func() { once.Do(lost.Done) })

	bridge := &fakeBridge{}
	bridge.onConnect = func(f *fakeBridge) error {
		c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")
		// ...and it drops again before Connect returns.
		c.OnConnectionStateChanged(ConnStateDisconnected, "AA:BB", "133")
		return nil
	}
	c.bridge = bridge

	err := c.Connect("SquareGolf(1234)", "")
	if err == nil {
		t.Fatal("expected Connect to report that the link had already dropped")
	}
	if c.IsConnected() {
		t.Fatal("IsConnected() is true on a link that died during connect")
	}

	done := make(chan struct{})
	go func() { lost.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("connection-lost callback never fired")
	}
}

// TestSolicitedDisconnectIsSilent proves the CAS suppression rule: our own
// Disconnect must not look like link loss, and a duplicate callback must not
// double-fire.
func TestSolicitedDisconnectIsSilent(t *testing.T) {
	c := NewClient(&fakeBridge{})
	defer c.Close()

	var calls int
	var mu sync.Mutex
	c.SetConnectionLostCallback(func() {
		mu.Lock()
		calls++
		mu.Unlock()
	})

	c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")
	if err := c.Disconnect(); err != nil {
		t.Fatalf("disconnect: %v", err)
	}
	c.OnConnectionStateChanged(ConnStateDisconnected, "AA:BB", "solicited")
	c.OnConnectionStateChanged(ConnStateDisconnected, "AA:BB", "duplicate")

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 0 {
		t.Fatalf("connection-lost fired %d times for a solicited disconnect, want 0", calls)
	}
}

// TestNotificationDataIsCopied is the regression test for the JNI aliasing rule.
// Kotlin's byte[] is released the instant DispatchNotification returns, so the
// slice must not be retained. The test mutates the caller's buffer immediately
// after the call, exactly as JNI reuse would.
func TestNotificationDataIsCopied(t *testing.T) {
	c := NewClient(&fakeBridge{})
	defer c.Close()
	c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")

	got := make(chan []byte, 1)
	if err := c.StartNotifications("uuid-1", func(b []byte) { got <- b }); err != nil {
		t.Fatalf("start notifications: %v", err)
	}

	buf := []byte{0x11, 0x22, 0x33}
	c.DispatchNotification("uuid-1", buf)
	for i := range buf {
		buf[i] = 0xff
	}

	select {
	case b := <-got:
		if b[0] != 0x11 || b[1] != 0x22 || b[2] != 0x33 {
			t.Fatalf("handler saw aliased JNI memory: %v", b)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("notification never delivered")
	}
}

// TestDiscoveryTableAndManufacturerData covers the Omni-vs-Home decision, which
// silently resolves every device as Home when the hex is empty.
func TestDiscoveryTableAndManufacturerData(t *testing.T) {
	c := NewClient(&fakeBridge{})
	defer c.Close()

	if err := c.StartScan("SquareGolf"); err != nil {
		t.Fatalf("start scan: %v", err)
	}
	c.OnDeviceDiscovered("SquareGolf(0001)", "AA:01", "")
	c.OnDeviceDiscovered("Someone's Earbuds", "BB:02", "cafe")
	c.OnDeviceDiscovered("SquareGolf(0002)", "AA:02", "3033303041")
	// A scan response with no manufacturer record must not blank an existing one.
	c.OnDeviceDiscovered("SquareGolf(0002)", "AA:02", "")

	names := c.GetDiscoveredDevices()
	if len(names) != 2 || names[0] != "SquareGolf(0001)" || names[1] != "SquareGolf(0002)" {
		t.Fatalf("discovery order or prefix filtering wrong: %v", names)
	}

	c.OnConnectionStateChanged(ConnStateConnected, "AA:02", "")
	if hex := c.GetConnectedDeviceManufacturerData(); hex != "3033303041" {
		t.Fatalf("manufacturer data = %q, want 3033303041", hex)
	}

	// A stale record must not survive a rescan: reporting the previous device's
	// hex would make DetectDeviceType resolve a Home unit as an Omni.
	if err := c.StartScan("SquareGolf"); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if hex := c.GetConnectedDeviceManufacturerData(); hex != "" {
		t.Fatalf("stale manufacturer data survived a rescan: %q", hex)
	}
}

// TestBridgeErrorNormalisesEmptyMessage covers the Throwable-with-no-message case,
// which gobind converts into the empty Go string.
func TestBridgeErrorNormalisesEmptyMessage(t *testing.T) {
	c := NewClient(&fakeBridge{connErr: emptyMessageError{}})
	defer c.Close()

	err := c.Connect("SquareGolf(1234)", "")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "kotlin threw an exception with no message") {
		t.Fatalf("unhelpful error text: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "SquareGolf(1234)") {
		t.Fatalf("error lost its subject: %q", err.Error())
	}
}

// TestNoBridgeIsDiagnosable proves a wiring mistake produces an error rather than
// a nil dereference.
func TestNoBridgeIsDiagnosable(t *testing.T) {
	c := NewClient(nil)
	defer c.Close()
	if err := c.Connect("x", ""); !errors.Is(err, ErrNoBridge) {
		t.Fatalf("Connect with no bridge = %v, want ErrNoBridge", err)
	}
	if err := c.StartScan("x"); !errors.Is(err, ErrNoBridge) {
		t.Fatalf("StartScan with no bridge = %v, want ErrNoBridge", err)
	}
}

// TestConcurrentTraffic is the race-detector target: it drives every entry point
// from several goroutines at once.
func TestConcurrentTraffic(t *testing.T) {
	c := NewClient(&fakeBridge{})
	defer c.Close()
	c.OnConnectionStateChanged(ConnStateConnected, "AA:BB", "")
	if err := c.StartNotifications("uuid-1", func([]byte) {}); err != nil {
		t.Fatalf("start notifications: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				switch (n + j) % 6 {
				case 0:
					c.DispatchNotification("uuid-1", []byte{byte(j)})
				case 1:
					c.OnDeviceDiscovered("SquareGolf(x)", "AA:BB", "3033")
				case 2:
					c.OnConnectionStateChanged(ConnStateScanning, "", "")
				case 3:
					_ = c.IsConnected()
				case 4:
					_ = c.GetDiscoveredDevices()
				case 5:
					_ = c.GetConnectedDeviceManufacturerData()
				}
			}
		}(i)
	}
	wg.Wait()
}
