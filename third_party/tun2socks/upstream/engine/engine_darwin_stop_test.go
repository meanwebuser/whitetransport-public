//go:build darwin

package engine

import (
	"fmt"
	"net"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestDarwinStopClosesDatagramFDWithoutBlocking(t *testing.T) {
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS endpoint: %v", err)
	}
	defer proxyListener.Close()

	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	engineFD, peerFD := fds[0], fds[1]
	defer unix.Close(peerFD)

	Insert(&Key{
		Proxy:    fmt.Sprintf("socks5://%s", proxyListener.Addr().String()),
		Device:   fmt.Sprintf("fd://%d", engineFD),
		MTU:      1500,
		LogLevel: "silent",
	})
	if err := Start(); err != nil {
		unix.Close(engineFD)
		t.Fatalf("engine start: %v", err)
	}

	// Let iobased.Endpoint enter its blocking Read before exercising Stop.
	time.Sleep(100 * time.Millisecond)
	stopDone := make(chan error, 1)
	go func() { stopDone <- Stop() }()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("engine stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		// Raw close is test cleanup only: it wakes the pre-fix blocked read so
		// the goroutine can finish before this RED test reports the failure.
		t.Logf("shutdown=%v", unix.Shutdown(engineFD, unix.SHUT_RDWR))
		_ = unix.Close(engineFD)
		select {
		case <-stopDone:
		case <-time.After(2 * time.Second):
			t.Fatal("engine stop remained blocked after descriptor cleanup")
		}
		t.Fatal("engine stop blocked on Darwin datagram fd")
	}

	if _, err := unix.FcntlInt(uintptr(engineFD), unix.F_GETFD, 0); err == nil {
		t.Fatalf("engine fd %d remained open after Stop", engineFD)
	}
}
