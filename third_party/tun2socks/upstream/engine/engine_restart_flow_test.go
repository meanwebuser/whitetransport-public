//go:build unix

package engine

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"

	"github.com/xjasonlyu/tun2socks/v2/core/device/iobased"
)

const restartFlowTarget = "198.51.100.10"

// TestEngineRestartReopensFDAndCarriesTCPPayload is a real engine regression:
// each cycle owns a socketpair-backed fd, reaches a loopback SOCKS echo server,
// stops the production engine, verifies the engine fd is closed, and starts a
// fresh cycle on the reused descriptor number.
func TestEngineRestartReopensFDAndCarriesTCPPayload(t *testing.T) {
	const payload = "restart-flow-proof"
	const targetPort = 443

	proxyPort, requests, stopProxy := startRestartSOCKSEcho(t, []byte(payload), restartFlowTarget, targetPort)
	t.Cleanup(stopProxy)

	var previousEngineFD int
	for round := 1; round <= 2; round++ {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
		if err != nil {
			t.Fatalf("round %d socketpair: %v", round, err)
		}
		engineFD, clientFD := fds[0], fds[1]
		if round == 2 && engineFD != previousEngineFD {
			t.Fatalf("round %d engine fd=%d, want reuse of round1 fd=%d", round, engineFD, previousEngineFD)
		}

		clientStack, closeClient := newRestartFlowClient(t, clientFD)
		started := false
		Insert(&Key{Proxy: fmt.Sprintf("socks5://127.0.0.1:%d", proxyPort), Device: fmt.Sprintf("fd://%d", engineFD), MTU: 1500, LogLevel: "silent"})
		if err := Start(); err != nil {
			closeClient()
			t.Fatalf("round %d engine start: %v", round, err)
		}
		started = true
		if round == 2 {
			// Darwin's fd-backed endpoint historically left the os.File wrapper
			// unreachable after raw close. Two collections run its finalizer and
			// expose any stale close against the freshly reused descriptor.
			runtime.GC()
			runtime.GC()
		}

		if err := restartFlowTCP(clientStack, restartFlowTarget, targetPort, []byte(payload)); err != nil {
			if started {
				_ = Stop()
			}
			closeClient()
			t.Fatalf("round %d TCP payload: %v", round, err)
		}
		if err := Stop(); err != nil {
			closeClient()
			t.Fatalf("round %d engine stop: %v", round, err)
		}
		started = false
		closeClient()
		if _, err := unix.FcntlInt(uintptr(engineFD), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
			t.Fatalf("round %d engine fd %d ownership err=%v, want EBADF", round, engineFD, err)
		}
		select {
		case request := <-requests:
			if request.host != restartFlowTarget || request.port != targetPort {
				t.Fatalf("round %d SOCKS target=(%s,%d), want=(%s,%d)", round, request.host, request.port, restartFlowTarget, targetPort)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("round %d SOCKS fixture saw no CONNECT request", round)
		}
		previousEngineFD = engineFD
	}
}

type restartSOCKSRequest struct {
	host string
	port int
}

func startRestartSOCKSEcho(t *testing.T, payload []byte, expectedHost string, expectedPort int) (int, <-chan restartSOCKSRequest, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen SOCKS fixture: %v", err)
	}
	requests := make(chan restartSOCKSRequest, 2)
	var workers sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		defer workers.Wait()
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				select {
				case <-stop:
					return
				default:
				}
				t.Errorf("accept SOCKS fixture: %v", acceptErr)
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer connection.Close()
				_ = connection.SetDeadline(time.Now().Add(5 * time.Second))
				if err := serveRestartSOCKS(connection, payload, expectedHost, expectedPort, requests); err != nil {
					t.Errorf("serve SOCKS fixture: %v", err)
				}
			}()
		}
	}()
	return listener.Addr().(*net.TCPAddr).Port, requests, func() {
		close(stop)
		_ = listener.Close()
		workers.Wait()
	}
}

func serveRestartSOCKS(connection net.Conn, payload []byte, expectedHost string, expectedPort int, requests chan<- restartSOCKSRequest) error {
	header := make([]byte, 3)
	if _, err := io.ReadFull(connection, header); err != nil {
		return err
	}
	if string(header) != "\x05\x01\x00" {
		return fmt.Errorf("handshake=%x", header)
	}
	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return err
	}
	requestHeader := make([]byte, 4)
	if _, err := io.ReadFull(connection, requestHeader); err != nil {
		return err
	}
	if requestHeader[0] != 5 || requestHeader[1] != 1 {
		return fmt.Errorf("request=%x", requestHeader)
	}
	var host string
	switch requestHeader[3] {
	case 1:
		address := make([]byte, 4)
		if _, err := io.ReadFull(connection, address); err != nil {
			return err
		}
		host = net.IP(address).String()
	case 4:
		address := make([]byte, 16)
		if _, err := io.ReadFull(connection, address); err != nil {
			return err
		}
		host = net.IP(address).String()
	default:
		return fmt.Errorf("address type=%d", requestHeader[3])
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(connection, portBytes); err != nil {
		return err
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	requests <- restartSOCKSRequest{host: host, port: port}
	if host != expectedHost || port != expectedPort {
		return fmt.Errorf("unexpected target (%s,%d)", host, port)
	}
	if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil {
		return err
	}
	_, err := connection.Write(received)
	return err
}

func newRestartFlowClient(t *testing.T, fd int) (*stack.Stack, func()) {
	t.Helper()
	file := os.NewFile(uintptr(fd), "restart-flow-client")
	endpoint, err := iobased.New(file, 1500, 0)
	if err != nil {
		t.Fatalf("create client endpoint: %v", err)
	}
	clientStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol},
	})
	if err := clientStack.CreateNIC(1, endpoint); err != nil {
		t.Fatalf("create client NIC: %v", err)
	}
	if err := clientStack.AddProtocolAddress(1, tcpip.ProtocolAddress{
		Protocol:          ipv4.ProtocolNumber,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}), PrefixLen: 24},
	}, stack.AddressProperties{}); err != nil {
		t.Fatalf("add client address: %v", err)
	}
	clientStack.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	return clientStack, func() {
		endpoint.Close()
		// Shutdown wakes an in-flight read on the socketpair before the
		// descriptor is closed, allowing Endpoint.Wait to prove cleanup.
		_ = unix.Shutdown(int(file.Fd()), unix.SHUT_RDWR)
		_ = file.Close()
		clientStack.Close()
		clientStack.Wait()
	}
}

func restartFlowTCP(clientStack *stack.Stack, host string, port int, payload []byte) error {
	address := tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{198, 51, 100, 10}), Port: uint16(port)}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	connection, err := gonet.DialContextTCP(ctx, clientStack, address, ipv4.ProtocolNumber)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(4 * time.Second))
	if _, err := connection.Write(payload); err != nil {
		return err
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil {
		return err
	}
	if string(received) != string(payload) {
		return fmt.Errorf("echo=%q, want=%q", received, payload)
	}
	_ = host
	return nil
}
