package punch

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"time"
)

// DefaultServers lists STUN endpoints verified reachable from a Russian
// vantage point (AS25513) on 2026-09-10. stun.l.google.com timed out from
// that vantage; sipnet (RU SIP operator) and cloudflare both answered
// binding requests. Public VK/Yandex/WB STUN servers do not exist —
// platform ICE credentials are issued per call session, not statically.
func DefaultServers() []string {
	return []string{
		"stun.sipnet.ru:3478",
		"stun.cloudflare.com:3478",
	}
}

const (
	stunMagicCookie uint32 = 0x2112A442
	stunHeaderLen          = 20
	stunBinding     uint16 = 0x0001
	stunBindingOK   uint16 = 0x0101
	attrXORMapped   uint16 = 0x0020
	attrMapped      uint16 = 0x0001
)

// stunBindingRequest builds a minimal RFC 5389 binding request with a fresh
// transaction id.
func stunBindingRequest() (datagram []byte, txn [12]byte, err error) {
	if _, err := rand.Read(txn[:]); err != nil {
		return nil, txn, fmt.Errorf("punch stun: txn id: %w", err)
	}
	datagram = make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(datagram[0:2], stunBinding)
	binary.BigEndian.PutUint16(datagram[2:4], 0) // no attributes
	binary.BigEndian.PutUint32(datagram[4:8], stunMagicCookie)
	copy(datagram[8:20], txn[:])
	return datagram, txn, nil
}

// stunParseMapped extracts the mapped address from a binding response.
// XOR-MAPPED-ADDRESS wins when both it and plain MAPPED-ADDRESS are present.
func stunParseMapped(response []byte, txn [12]byte) (*net.UDPAddr, error) {
	if len(response) < stunHeaderLen {
		return nil, fmt.Errorf("punch stun: short response %d", len(response))
	}
	if typ := binary.BigEndian.Uint16(response[0:2]); typ != stunBindingOK {
		return nil, fmt.Errorf("punch stun: unexpected type 0x%04x", typ)
	}
	if binary.BigEndian.Uint32(response[4:8]) != stunMagicCookie {
		return nil, fmt.Errorf("punch stun: bad cookie")
	}
	if string(response[8:20]) != string(txn[:]) {
		return nil, fmt.Errorf("punch stun: transaction mismatch")
	}
	body := int(binary.BigEndian.Uint16(response[2:4]))
	end := stunHeaderLen + body
	if end > len(response) {
		return nil, fmt.Errorf("punch stun: body overruns datagram")
	}
	var fallback *net.UDPAddr
	for off := stunHeaderLen; off+4 <= end; {
		typ := binary.BigEndian.Uint16(response[off : off+2])
		alen := int(binary.BigEndian.Uint16(response[off+2 : off+4]))
		valStart, valEnd := off+4, off+4+alen
		if valEnd > end {
			return nil, fmt.Errorf("punch stun: attribute overruns body")
		}
		if typ == attrXORMapped || typ == attrMapped {
			addr, err := parseStunAddress(response[valStart:valEnd], typ == attrXORMapped, txn)
			if err == nil {
				if typ == attrXORMapped {
					return addr, nil
				}
				fallback = addr
			}
		}
		off = valEnd + (4-alen%4)%4
	}
	if fallback != nil {
		return fallback, nil
	}
	return nil, fmt.Errorf("punch stun: no mapped address")
}

// parseStunAddress decodes one MAPPED-ADDRESS / XOR-MAPPED-ADDRESS value.
func parseStunAddress(val []byte, xor bool, txn [12]byte) (*net.UDPAddr, error) {
	if len(val) < 8 {
		return nil, fmt.Errorf("punch stun: address value too short")
	}
	var mask []byte
	if xor {
		mask = make([]byte, 16)
		binary.BigEndian.PutUint32(mask[:4], stunMagicCookie)
		copy(mask[4:8], txn[:4])
	}
	port := binary.BigEndian.Uint16(val[2:4])
	if xor {
		port ^= uint16(stunMagicCookie >> 16)
	}
	switch val[1] {
	case 0x01:
		ip := append([]byte(nil), val[4:8]...)
		if xor {
			for i := range ip {
				ip[i] ^= mask[i]
			}
		}
		return &net.UDPAddr{IP: net.IP(ip), Port: int(port)}, nil
	case 0x02:
		if len(val) < 20 {
			return nil, fmt.Errorf("punch stun: v6 address value too short")
		}
		ip := append([]byte(nil), val[8:20]...)
		if xor {
			for i := range ip {
				ip[i] ^= mask[i]
			}
		}
		return &net.UDPAddr{IP: net.IP(ip), Port: int(port)}, nil
	}
	return nil, fmt.Errorf("punch stun: unknown address family %d", val[1])
}

// stunQuery sends binding requests over the punch socket itself and returns
// the server-reflexive address the STUN server observed. Reusing the punch
// socket is what makes the discovered mapping identical to the address a
// peer's probes must target. Queries against different servers are issued
// sequentially over the shared socket.
func stunQuery(conn *net.UDPConn, server string, timeout time.Duration) (*net.UDPAddr, error) {
	req, txn, err := stunBindingRequest()
	if err != nil {
		return nil, err
	}
	raddr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("punch stun: resolve %s: %w", server, err)
	}
	buf := make([]byte, 1500)
	for attempt := 0; attempt < 2; attempt++ {
		if err := conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
			return nil, fmt.Errorf("punch stun: deadline: %w", err)
		}
		if _, err := conn.WriteToUDP(req, raddr); err != nil {
			return nil, fmt.Errorf("punch stun: write %s: %w", server, err)
		}
		for {
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					break // retransmit once, then give up on this server
				}
				return nil, fmt.Errorf("punch stun: read: %w", err)
			}
			if from == nil || from.IP == nil || !from.IP.Equal(raddr.IP) {
				continue // unrelated traffic on the shared punch socket
			}
			if addr, err := stunParseMapped(buf[:n], txn); err == nil {
				return addr, nil
			}
			// Malformed datagram from the right server: keep reading.
		}
	}
	return nil, fmt.Errorf("punch stun: %s: timeout", server)
}

// hostCandidates lists local IPv4 candidates for the given punch port.
// Loopback is included so same-host canaries punch over loopback.
func hostCandidates(port int) []Candidate {
	var out []Candidate
	ifaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipnet.IP.To4()
			if ip4 == nil {
				continue // IPv4 candidates only in this slice
			}
			out = append(out, Candidate{
				Addr: (&net.UDPAddr{IP: ip4, Port: port}).String(),
				Typ:  "host",
			})
		}
	}
	return out
}
