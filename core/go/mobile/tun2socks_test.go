package mobile

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFormatTun2SocksStartLogRedactsProxyDetails(t *testing.T) {
	got := formatTun2SocksStartLog(7, 1500)

	if got != "[tun2socks] starting fd=7 mtu=1500 proxy=<redacted>" {
		t.Fatalf("unexpected log format: %q", got)
	}
	for _, banned := range []string{"alice", "s3cr3t", "127.0.0.1:1080", ":1080"} {
		if strings.Contains(got, banned) {
			t.Fatalf("log leaked %q: %q", banned, got)
		}
	}
}

func TestStartTun2SocksSessionResetsTunActiveOnRunnerError(t *testing.T) {
	tunMu.Lock()
	tunState = tun2SocksIdle
	tunDone = nil
	tunSession = nil
	tunMu.Unlock()

	var logBuf bytes.Buffer
	origWriter := log.Writer()
	origFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(origWriter)
		log.SetFlags(origFlags)
	})

	runner := &failingTun2SocksRunner{startErr: errors.New("boom")}
	err := startTun2SocksSession(runner, 11, 1500, 1080, "alice", "s3cr3t")
	if !errors.Is(err, runner.startErr) {
		t.Fatalf("expected %v, got %v", runner.startErr, err)
	}
	if runner.insertedLevel != "warn" {
		t.Fatalf("engine log level=%q, want warn to suppress proxy endpoint logs", runner.insertedLevel)
	}

	tunMu.Lock()
	active := tunState != tun2SocksIdle
	tunMu.Unlock()
	if active {
		t.Fatal("tunActive stayed true after runner error")
	}
	if !strings.Contains(logBuf.String(), "proxy=<redacted>") {
		t.Fatalf("start log was not redacted: %q", logBuf.String())
	}
	if strings.Contains(logBuf.String(), "alice") || strings.Contains(logBuf.String(), "s3cr3t") || strings.Contains(logBuf.String(), "127.0.0.1:1080") {
		t.Fatalf("start log leaked sensitive data: %q", logBuf.String())
	}
}

func TestStartTun2SocksSessionKeepsNonblockingEngineRunningUntilExplicitStop(t *testing.T) {
	tunMu.Lock()
	tunState = tun2SocksIdle
	tunDone = nil
	tunSession = nil
	tunMu.Unlock()

	runner := &nonblockingTun2SocksRunner{}
	if err := startTun2SocksSession(runner, 21, 1500, 1085, "", ""); err != nil {
		t.Fatalf("start nonblocking tun2socks engine: %v", err)
	}
	tunMu.Lock()
	stateAfterStart := tunState
	doneAfterStart := tunDone
	tunMu.Unlock()
	if stateAfterStart != tun2SocksRunning {
		t.Fatalf("state after nonblocking Start = %v, want Running", stateAfterStart)
	}
	select {
	case <-doneAfterStart:
		t.Fatal("session completion closed when nonblocking Start returned")
	default:
	}

	if err := stopTun2SocksSession(); err != nil {
		t.Fatalf("stop nonblocking tun2socks engine: %v", err)
	}
	if err := stopTun2SocksSession(); err != nil {
		t.Fatalf("repeat stop nonblocking tun2socks engine: %v", err)
	}
	starts, stops := runner.counts()
	if starts != 1 || stops != 1 {
		t.Fatalf("runner starts=%d stops=%d, want exactly one of each", starts, stops)
	}
	tunMu.Lock()
	stateAfterStop := tunState
	tunMu.Unlock()
	if stateAfterStop != tun2SocksIdle {
		t.Fatalf("state after explicit Stop = %v, want Idle", stateAfterStop)
	}
}

func TestStopTun2SocksSessionReportsRunnerError(t *testing.T) {
	tunMu.Lock()
	tunState = tun2SocksIdle
	tunDone = nil
	tunSession = nil
	tunMu.Unlock()

	stopErr := errors.New("native stop failed")
	runner := &nonblockingTun2SocksRunner{stopErr: stopErr}
	if err := startTun2SocksSession(runner, 22, 1500, 1085, "", ""); err != nil {
		t.Fatalf("start erroring-stop runner: %v", err)
	}
	if err := stopTun2SocksSession(); !errors.Is(err, stopErr) {
		t.Fatalf("stop error = %v, want %v", err, stopErr)
	}
	_, stops := runner.counts()
	if stops != 1 {
		t.Fatalf("runner stops=%d, want exactly one", stops)
	}
}

func TestStartTun2SocksSessionRejectsRestartWhileStopping(t *testing.T) {
	tunMu.Lock()
	tunState = tun2SocksIdle
	tunDone = nil
	tunSession = nil
	tunMu.Unlock()

	runner := &blockingTun2SocksRunner{started: make(chan struct{})}
	startDone := make(chan error, 1)
	go func() {
		startDone <- startTun2SocksSession(runner, 11, 1500, 1080, "alice", "s3cr3t")
	}()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("first tun2socks session did not start")
	}

	stopEntered := make(chan struct{})
	allowStop := make(chan struct{})
	runner.stopEntered = stopEntered
	runner.allowStop = allowStop
	stopDone := make(chan struct{})
	go func() {
		_ = stopTun2SocksSession()
		close(stopDone)
	}()
	select {
	case <-stopEntered:
	case <-time.After(time.Second):
		t.Fatal("tun2socks stop did not enter")
	}

	restartErr := startTun2SocksSession(&failingTun2SocksRunner{startErr: errors.New("must not start")}, 12, 1500, 1081, "bob", "other-secret")
	if restartErr == nil || !strings.Contains(restartErr.Error(), "already running") {
		t.Fatalf("restart during stop err=%v, want already running", restartErr)
	}
	close(allowStop)
	select {
	case err := <-startDone:
		if err != nil {
			t.Fatalf("first tun2socks session: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first tun2socks session did not return")
	}
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("tun2socks stop did not return")
	}
}

type failingTun2SocksRunner struct {
	insertedProxy  string
	insertedDevice string
	insertedMTU    int
	insertedLevel  string
	startErr       error
}

func (r *failingTun2SocksRunner) Insert(proxy string, device string, mtu int, logLevel string) {
	r.insertedProxy = proxy
	r.insertedDevice = device
	r.insertedMTU = mtu
	r.insertedLevel = logLevel
}

func (r *failingTun2SocksRunner) Start() error {
	return r.startErr
}

func (r *failingTun2SocksRunner) Stop() error { return nil }

type blockingTun2SocksRunner struct {
	started     chan struct{}
	stopEntered chan struct{}
	allowStop   chan struct{}
	once        sync.Once
	stopOnce    sync.Once
}

type nonblockingTun2SocksRunner struct {
	mu      sync.Mutex
	starts  int
	stops   int
	stopErr error
}

func (runner *nonblockingTun2SocksRunner) Insert(string, string, int, string) {}

func (runner *nonblockingTun2SocksRunner) Start() error {
	runner.mu.Lock()
	runner.starts++
	runner.mu.Unlock()
	return nil
}

func (runner *nonblockingTun2SocksRunner) Stop() error {
	runner.mu.Lock()
	runner.stops++
	runner.mu.Unlock()
	return runner.stopErr
}

func (runner *nonblockingTun2SocksRunner) counts() (int, int) {
	runner.mu.Lock()
	defer runner.mu.Unlock()
	return runner.starts, runner.stops
}

func (runner *blockingTun2SocksRunner) Insert(string, string, int, string) {}

func (runner *blockingTun2SocksRunner) Start() error {
	runner.once.Do(func() { close(runner.started) })
	return nil
}

func (runner *blockingTun2SocksRunner) Stop() error {
	runner.stopOnce.Do(func() {
		if runner.stopEntered != nil {
			close(runner.stopEntered)
		}
		if runner.allowStop != nil {
			<-runner.allowStop
		}
	})
	return nil
}
