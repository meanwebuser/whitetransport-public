package mobile

import (
	"fmt"
	"log"
	"sync"
)

var (
	tunMu      sync.Mutex
	tunState   tun2SocksState
	tunDone    chan struct{}
	tunSession *tun2SocksSession
)

type tun2SocksState uint8

const (
	tun2SocksIdle tun2SocksState = iota
	tun2SocksRunning
	tun2SocksStopping
)

type tun2SocksRunner interface {
	Insert(proxy string, device string, mtu int, logLevel string)
	Start() error
	Stop() error
}

type tun2SocksSession struct {
	runner     tun2SocksRunner
	done       chan struct{}
	startDone  chan struct{}
	finishOnce sync.Once
	stopOnce   sync.Once

	mu       sync.Mutex
	startErr error
	stopErr  error
}

const tun2SocksEngineLogLevel = "warn"

func formatTun2SocksStartLog(fd int64, mtu int64) string {
	return fmt.Sprintf("[tun2socks] starting fd=%d mtu=%d proxy=<redacted>", fd, mtu)
}

func buildTun2SocksProxy(socksPort int64, socksUser string, socksPass string) string {
	if socksUser != "" {
		return fmt.Sprintf("socks5://%s:%s@127.0.0.1:%d", socksUser, socksPass, socksPort)
	}
	return fmt.Sprintf("socks5://127.0.0.1:%d", socksPort)
}

func startTun2SocksSession(runner tun2SocksRunner, fd int64, mtu int64, socksPort int64, socksUser string, socksPass string) error {
	tunMu.Lock()
	if tunState != tun2SocksIdle {
		tunMu.Unlock()
		return fmt.Errorf("tun2socks already running")
	}
	tunState = tun2SocksRunning
	session := &tun2SocksSession{runner: runner, done: make(chan struct{}), startDone: make(chan struct{})}
	tunSession = session
	tunDone = session.done
	tunMu.Unlock()

	proxy := buildTun2SocksProxy(socksPort, socksUser, socksPass)
	log.Print(formatTun2SocksStartLog(fd, mtu))

	runner.Insert(proxy, fmt.Sprintf("fd://%d", fd), int(mtu), tun2SocksEngineLogLevel)
	err := runner.Start()
	session.mu.Lock()
	session.startErr = err
	close(session.startDone)
	session.mu.Unlock()
	if err != nil {
		session.finish()
		return err
	}
	tunMu.Lock()
	stillRunning := tunSession == session && tunState == tun2SocksRunning
	tunMu.Unlock()
	if stillRunning {
		log.Printf("[tun2socks] engine running")
	}
	return nil
}

func stopTun2SocksSession() error {
	tunMu.Lock()
	switch tunState {
	case tun2SocksIdle:
		tunMu.Unlock()
		return nil
	default:
		tunState = tun2SocksStopping
	}
	session := tunSession
	tunMu.Unlock()
	if session == nil {
		return fmt.Errorf("tun2socks lifecycle is inconsistent")
	}

	<-session.startDone
	session.mu.Lock()
	startErr := session.startErr
	session.mu.Unlock()
	if startErr != nil {
		session.finish()
		<-session.done
		return startErr
	}
	session.stopOnce.Do(func() {
		stopErr := session.runner.Stop()
		session.mu.Lock()
		session.stopErr = stopErr
		session.mu.Unlock()
		session.finish()
	})
	<-session.done
	session.mu.Lock()
	stopErr := session.stopErr
	session.mu.Unlock()
	if stopErr != nil {
		log.Printf("[tun2socks] stop failed")
		return stopErr
	}
	log.Printf("[tun2socks] stopped cleanly")
	return nil
}

func (session *tun2SocksSession) finish() {
	session.finishOnce.Do(func() {
		tunMu.Lock()
		if tunSession == session {
			tunState = tun2SocksIdle
			tunSession = nil
			if tunDone == session.done {
				tunDone = nil
			}
		}
		close(session.done)
		tunMu.Unlock()
	})
}
