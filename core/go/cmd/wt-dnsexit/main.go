// Command wt-dnsexit runs the authoritative DNS exit for the dns.txt
// carrier. Delegate a zone (for example wt.example.com) to the host running
// this binary, then point clients at any whitelisted recursive resolver —
// Yandex 77.88.8.8 under whitelist censorship — and their TXT queries will
// recurse here.
//
// Usage:
//
//	wt-dnsexit -zone wt.example.com [-listen :53] [-disable-tcp]
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
)

func main() {
	zone := flag.String("zone", "", "delegated DNS zone to serve, e.g. wt.example.com (required)")
	listen := flag.String("listen", ":53", "UDP/TCP listen address")
	disableTCP := flag.Bool("disable-tcp", false, "do not serve DNS-over-TCP")
	flag.Parse()

	if *zone == "" {
		fmt.Fprintln(os.Stderr, "wt-dnsexit: -zone is required")
		flag.Usage()
		os.Exit(2)
	}

	server, err := carriers.NewDNSTXTServer(carriers.DNSTXTServerConfig{
		Zone: *zone,
		LogFn: func(format string, args ...any) {
			log.Printf("[wt-dnsexit] "+format, args...)
		},
	})
	if err != nil {
		log.Fatalf("wt-dnsexit: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	udpConn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		log.Fatalf("wt-dnsexit: udp listen %s: %v", *listen, err)
	}
	log.Printf("wt-dnsexit: serving zone %s on udp %s", server.Zone(), udpConn.LocalAddr())
	serveDone := make(chan error, 2)
	go func() { serveDone <- server.ServeUDP(ctx, udpConn) }()

	if !*disableTCP {
		tcpListener, err := net.Listen("tcp", *listen)
		if err != nil {
			log.Fatalf("wt-dnsexit: tcp listen %s: %v", *listen, err)
		}
		log.Printf("wt-dnsexit: serving zone %s on tcp %s", server.Zone(), tcpListener.Addr())
		go func() { serveDone <- server.ServeTCP(ctx, tcpListener) }()
	}

	select {
	case err := <-serveDone:
		if ctx.Err() == nil && err != nil {
			log.Fatalf("wt-dnsexit: server failed: %v", err)
		}
	case <-ctx.Done():
	}
	log.Printf("wt-dnsexit: stopped")
}
