//go:build e2e

// This is the Phase 1 pipeline proof: simulator -> LaunchMonitor -> connectapi ->
// a fake GSPro Open Connect listener, with no hardware and no Android.
//
// It is behind the e2e build tag because it takes about a minute of wall clock
// (the simulator's shot cycle is ~11.5s and the disarm assertion is a negative one
// that can only be made by waiting). `go test -race ./...` must stay fast enough to
// run before every commit, so this is opt-in:
//
//	go test -tags e2e -run TestPipeline ./internal/transport/android/
//
// It touches process-wide singletons (core.GetInstance and friends) and therefore
// cannot run alongside another test that builds an engine.
package android

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordingListener struct {
	mu     sync.Mutex
	status []int32
	shots  int
}

func (r *recordingListener) OnStatus(code int32, detail string) error {
	r.mu.Lock()
	r.status = append(r.status, code)
	r.mu.Unlock()
	return nil
}

func (r *recordingListener) OnShot(a, b, c float64, d int32, e float64) error {
	r.mu.Lock()
	r.shots++
	r.mu.Unlock()
	return nil
}

func (r *recordingListener) OnLog(string) error { return nil }

func (r *recordingListener) shotCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shots
}

func (r *recordingListener) sawStatus(code int32) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.status {
		if s == code {
			return true
		}
	}
	return false
}

// fakeGSPro is a minimal Open Connect server: it announces readiness, counts
// messages carrying ball data, and acknowledges each one.
type fakeGSPro struct {
	ln net.Listener

	mu       sync.Mutex
	messages int
	withBall int
}

func startFakeGSPro(t *testing.T) *fakeGSPro {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	g := &fakeGSPro{ln: ln}
	go g.accept()
	return g
}

func (g *fakeGSPro) port() int { return g.ln.Addr().(*net.TCPAddr).Port }

func (g *fakeGSPro) accept() {
	for {
		conn, err := g.ln.Accept()
		if err != nil {
			return
		}
		go g.serve(conn)
	}
}

func (g *fakeGSPro) serve(conn net.Conn) {
	defer conn.Close()
	// GSPro announces readiness unprompted; this is what makes connectapi arm ball
	// detection, and therefore what makes the simulator free-run.
	_, _ = conn.Write([]byte(`{"Code":201,"Message":"GSPro Player Information","Player":{"Handed":"RH","Club":"DR"}}`))

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	sc.Split(splitJSONObjects)
	for sc.Scan() {
		raw := sc.Text()
		var m map[string]any
		hasBall := false
		if err := json.Unmarshal([]byte(raw), &m); err == nil {
			if _, ok := m["BallData"]; ok {
				hasBall = true
			}
		} else {
			hasBall = strings.Contains(raw, "BallData")
		}
		g.mu.Lock()
		g.messages++
		if hasBall {
			g.withBall++
		}
		g.mu.Unlock()
		_, _ = conn.Write([]byte(`{"Code":200,"Message":"Club & Ball Data received","Player":null}`))
	}
}

func (g *fakeGSPro) counts() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.messages, g.withBall
}

// splitJSONObjects frames a stream of concatenated top-level JSON objects.
func splitJSONObjects(data []byte, atEOF bool) (int, []byte, error) {
	depth, inStr, esc := 0, false, false
	for i, b := range data {
		switch {
		case esc:
			esc = false
		case b == '\\' && inStr:
			esc = true
		case b == '"':
			inStr = !inStr
		case inStr:
		case b == '{':
			depth++
		case b == '}':
			depth--
			if depth == 0 {
				return i + 1, data[:i+1], nil
			}
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func waitFor(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s after %v", what, d)
}

func TestPipelineSimulatorToGSPro(t *testing.T) {
	gs := startFakeGSPro(t)
	defer gs.ln.Close()

	lis := &recordingListener{}
	eng, err := Start(Config{
		GSProHost:    "127.0.0.1",
		GSProPort:    gs.port(),
		SimulateOmni: true,
		Listener:     lis,
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Arming before the launch monitor is connected must be refused, not attempted.
	if err := eng.Arm(); err == nil {
		t.Fatal("Arm succeeded before the launch monitor was connected")
	}

	// Device leg first: GSPro announces readiness unprompted and connectapi arms
	// ball detection the moment it arrives.
	if err := eng.ConnectDevice(); err != nil {
		t.Fatalf("connect device: %v", err)
	}
	waitFor(t, "launch monitor connected", 30*time.Second, func() bool {
		return eng.LaunchMonitorStatus() == "connected"
	})
	if !lis.sawStatus(StatusLMConnected) {
		t.Fatal("listener never saw StatusLMConnected")
	}

	if err := eng.ConnectGSPro(); err != nil {
		t.Fatalf("connect gspro: %v", err)
	}
	waitFor(t, "gspro connected", 20*time.Second, func() bool {
		return eng.GSProStatus() == "connected"
	})

	// One shot must reach the fake GSPro with ball data attached.
	waitFor(t, "a shot with ball data at GSPro", 40*time.Second, func() bool {
		_, ball := gs.counts()
		return ball >= 1
	})
	if lis.shotCount() < 1 {
		t.Fatal("listener never saw OnShot")
	}

	// Disarm must hold against the simulator.
	before := lis.shotCount()
	if err := eng.Disarm(); err != nil {
		t.Fatalf("disarm: %v", err)
	}
	time.Sleep(20 * time.Second)
	if after := lis.shotCount(); after != before {
		t.Fatalf("a shot landed after Disarm: %d -> %d", before, after)
	}

	msgs, ball := gs.counts()
	t.Logf("fake GSPro received %d messages, %d with ball data; listener saw %d shots", msgs, ball, lis.shotCount())

	eng.Stop()
	if eng.IsRunning() {
		t.Fatal("IsRunning() still true after Stop")
	}
}
