package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"time"

	"github.com/xjasonlyu/tun2socks/v2/core/device/iobased"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"
)

const (
	nicID       tcpip.NICID = 1
	tcpPort                 = 443
	udpPort                 = 53
	testNetIPv4             = "198.51.100.10"
	testNetIPv6             = "2001:db8::10"
)

var (
	testNetV4 = netip.MustParseAddr(testNetIPv4)
	testNetV6 = netip.MustParseAddr(testNetIPv6)
)

type result struct {
	IPv4TCP bool `json:"ipv4_tcp"`
	IPv6TCP bool `json:"ipv6_tcp"`
	IPv4UDP bool `json:"ipv4_udp"`
	IPv6UDP bool `json:"ipv6_udp"`
}

func main() {
	if len(os.Args) != 2 {
		fatalf("usage: client <fd>")
	}
	networkStack, closeStack := mustStack(mustInt(os.Args[1]))
	defer closeStack()

	tcpPayload := []byte("wt-tcp-proof")
	udpPayload := []byte("wt-udp-proof")
	round := os.Getenv("WT_TEST_ROUND")
	if round == "" {
		round = "0"
	}
	fmt.Fprintf(os.Stderr, "client=round round=%s result=begin\n", round)

	ipv4TCP := tcpEcho(networkStack, testNetV4, tcpPort, tcpPayload)
	ipv6TCP := tcpEcho(networkStack, testNetV6, tcpPort, tcpPayload)
	ipv4UDP := udpEcho(networkStack, testNetV4, udpPort, udpPayload)
	ipv6UDP := udpEcho(networkStack, testNetV6, udpPort, udpPayload)
	proof := result{IPv4TCP: ipv4TCP, IPv6TCP: ipv6TCP, IPv4UDP: ipv4UDP, IPv6UDP: ipv6UDP}
	if !proof.IPv4TCP || !proof.IPv6TCP || !proof.IPv4UDP || !proof.IPv6UDP {
		fatalf("incomplete packet proof: %+v", proof)
	}
	encoded, err := json.Marshal(proof)
	if err != nil {
		fatalf("encode proof: %v", err)
	}
	fmt.Println(string(encoded))
	fmt.Fprintf(os.Stderr, "client=round round=%s result=success\n", round)
	// The runner owns the packet descriptor and performs the authoritative stop
	// and EBADF check after this process exits.
	os.Exit(0)
}

func mustStack(fd int) (*stack.Stack, func()) {
	file := os.NewFile(uintptr(fd), "packet-flow-client")
	if file == nil {
		fatalf("invalid packet-flow descriptor")
	}
	endpoint, err := iobased.New(file, 1_500, 0)
	if err != nil {
		fatalf("create packet endpoint: %v", err)
	}
	networkStack := stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol, ipv6.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if err := networkStack.CreateNIC(nicID, endpoint); err != nil {
		fatalf("create NIC: %v", err)
	}
	addAddress(networkStack, ipv4.ProtocolNumber, tcpip.AddrFrom4([4]byte{10, 0, 0, 2}), 24)
	addAddress(networkStack, ipv6.ProtocolNumber, tcpip.AddrFrom16([16]byte{0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 2}), 64)
	networkStack.SetRouteTable([]tcpip.Route{
		{Destination: header.IPv4EmptySubnet, NIC: nicID},
		{Destination: header.IPv6EmptySubnet, NIC: nicID},
	})
	return networkStack, func() {
		_ = file.Close()
		endpoint.Close()
		networkStack.Close()
		networkStack.Wait()
	}
}

func addAddress(networkStack *stack.Stack, protocol tcpip.NetworkProtocolNumber, address tcpip.Address, prefix int) {
	if err := networkStack.AddProtocolAddress(nicID, tcpip.ProtocolAddress{
		Protocol:          protocol,
		AddressWithPrefix: tcpip.AddressWithPrefix{Address: address, PrefixLen: prefix},
	}, stack.AddressProperties{}); err != nil {
		fatalf("add protocol address: %v", err)
	}
}

func tcpEcho(networkStack *stack.Stack, address netip.Addr, port int, payload []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	logProbe("start", "tcp", address, port, 0, "begin")
	connection, err := gonet.DialContextTCP(ctx, networkStack, fullAddress(address, port), networkProtocol(address))
	if err != nil {
		fatalf("tcp dial family=%s: %v", familyName(address), err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := connection.Write(payload); err != nil {
		fatalf("tcp write family=%s: %v", familyName(address), err)
	}
	logProbe("tx", "tcp", address, port, len(payload), "sent")
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil {
		fatalf("tcp read family=%s: %v", familyName(address), err)
	}
	if string(received) != string(payload) {
		logProbe("rx", "tcp", address, port, len(received), "mismatch")
		return false
	}
	logProbe("rx", "tcp", address, port, len(received), "echo")
	return true
}

func udpEcho(networkStack *stack.Stack, address netip.Addr, port int, payload []byte) bool {
	logProbe("start", "udp", address, port, 0, "begin")
	connection, err := gonet.DialUDP(networkStack, nil, pointer(fullAddress(address, port)), networkProtocol(address))
	if err != nil {
		fatalf("udp dial family=%s: %v", familyName(address), err)
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(8 * time.Second))
	if _, err := connection.Write(payload); err != nil {
		fatalf("udp write family=%s: %v", familyName(address), err)
	}
	logProbe("tx", "udp", address, port, len(payload), "sent")
	received := make([]byte, len(payload))
	count, err := connection.Read(received)
	if err != nil {
		fatalf("udp read family=%s: %v", familyName(address), err)
	}
	if string(received[:count]) != string(payload) {
		logProbe("rx", "udp", address, port, count, "mismatch")
		return false
	}
	logProbe("rx", "udp", address, port, count, "echo")
	return true
}

func fullAddress(address netip.Addr, port int) tcpip.FullAddress {
	return tcpip.FullAddress{NIC: nicID, Addr: tcpip.AddrFromSlice(address.AsSlice()), Port: uint16(port)}
}

func networkProtocol(address netip.Addr) tcpip.NetworkProtocolNumber {
	if address.Is4() {
		return ipv4.ProtocolNumber
	}
	return ipv6.ProtocolNumber
}

func familyName(address netip.Addr) string {
	if address.Is4() {
		return "ipv4"
	}
	return "ipv6"
}

func pointer[T any](value T) *T { return &value }

func mustInt(value string) int {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		fatalf("parse integer: %v", err)
	}
	return parsed
}

func fatalf(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}

func logProbe(stage string, protocol string, address netip.Addr, port int, length int, result string) {
	family := familyName(address)
	ipVersion := "4"
	if address.Is6() {
		ipVersion = "6"
	}
	round := os.Getenv("WT_TEST_ROUND")
	if round == "" {
		round = "0"
	}
	fmt.Fprintf(os.Stderr, "client=probe round=%s stage=%s family=%s ip_version=%s protocol=%s port=%d length=%d result=%s\n", round, stage, family, ipVersion, protocol, port, length, result)
}
