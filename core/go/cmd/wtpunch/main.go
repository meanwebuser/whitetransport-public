// Command wtpunch is the P2P punch canary. Two instances exchange punch
// signaling through a shared file.mailbox directory, gather candidates
// (host + STUN reflexive via stun.sipnet.ru / stun.cloudflare.com), punch
// a direct UDP path, and measure RTT. On punch failure the tool exits with
// status 2 after printing the hub-as-TURN fallback reminder — carrier
// egress paths are untouched by design.
//
// Two-machine usage (shared dir via sshfs/scp is unnecessary: point both
// sides at their own mailbox names, the directory itself must be shared):
//
//	# free-internet side
//	wtpunch -role answer -dir /shared/mailbox -self B -peer A -token s3cret
//	# censored side
//	wtpunch -role offer  -dir /shared/mailbox -self A -peer B -token s3cret
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/punch"
)

// runRelay glues exactly two inbound signaling connections byte-for-byte:
// the smallest possible signaling hub for canaries where neither punch
// endpoint is publicly reachable (both sides dial out to the relay).
func runRelay(ctx context.Context, addr string, logf func(string, ...any)) {
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		log.Fatalf("[wtpunch] relay listen: %v", err)
	}
	defer ln.Close()
	logf("relay: listening on %s, waiting for two dialers", addr)

	type accepted struct {
		conn net.Conn
	}
	ch := make(chan accepted, 2)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			ch <- accepted{conn: conn}
		}
	}()
	pick := func(label string) net.Conn {
		select {
		case a := <-ch:
			logf("relay: %s connected from %s", label, a.conn.RemoteAddr())
			return a.conn
		case <-ctx.Done():
			log.Fatalf("[wtpunch] relay: context done before %s", label)
			return nil
		}
	}
	first, second := pick("peer 1"), pick("peer 2")
	defer first.Close()
	defer second.Close()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(first, second); done <- struct{}{} }()
	go func() { _, _ = io.Copy(second, first); done <- struct{}{} }()
	<-done
	first.Close()
	second.Close()
	logf("relay: session ended")
}

func main() {
	var (
		role       = flag.String("role", "", "offer (initiates punch), answer (responds) or relay (signaling glue)")
		signalMode = flag.String("signal", "file", "signaling: file (shared mailbox dir), dial (connect to -addr), listen (accept on -addr) or relay (glue two inbound conns on -addr)")
		dir        = flag.String("dir", "", "shared file.mailbox directory for signaling (file mode)")
		signalAddr = flag.String("addr", "", "signaling address: dial target or listen/relay bind address")
		selfID     = flag.String("self", "", "this node's punch id (file mode)")
		peerID     = flag.String("peer", "", "peer node's punch id (file mode)")
		token      = flag.String("token", "", "shared pairwise secret (seals signaling, derives session key)")
		stun       = flag.String("stun", "", "comma-separated STUN servers (default: sipnet + cloudflare)")
		count      = flag.Int("count", 10, "pings to send after punch (offer role)")
		timeout    = flag.Duration("timeout", 15*time.Second, "punch deadline")
		port       = flag.Int("port", 0, "fixed local UDP port for the punch socket (0 = random)")
	)
	flag.Parse()

	switch {
	case *role != "offer" && *role != "answer" && *role != "relay":
		log.Fatalf("[wtpunch] -role must be offer, answer or relay")
	case *signalMode != "file" && *signalMode != "dial" && *signalMode != "listen" && *signalMode != "relay":
		log.Fatalf("[wtpunch] -signal must be file, dial, listen or relay")
	case *token == "":
		log.Fatalf("[wtpunch] -token is required")
	case *signalMode == "file" && (*dir == "" || *selfID == "" || *peerID == ""):
		log.Fatalf("[wtpunch] file signaling requires -dir, -self and -peer")
	case *signalMode != "file" && *signalAddr == "":
		log.Fatalf("[wtpunch] %s signaling requires -addr", *signalMode)
	}

	logf := func(format string, args ...any) {
		log.Printf("[wtpunch] "+format, args...)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var sig punch.Signaling
	var err error
	switch *signalMode {
	case "file":
		carrier, ferr := carriers.NewFileMailboxCarrier(carriers.FileMailboxConfig{Dir: *dir})
		if ferr != nil {
			log.Fatalf("[wtpunch] mailbox: %v", ferr)
		}
		sig, err = punch.NewFabricSignaling(carrier, *token, *selfID, *peerID, logf)
		if err != nil {
			log.Fatalf("[wtpunch] signaling: %v", err)
		}
	case "dial":
		logf("signaling: dialing %s", *signalAddr)
		sig, err = tcpDialSignaling(ctx, *signalAddr, *token)
	case "listen":
		logf("signaling: listening on %s", *signalAddr)
		sig, err = tcpListenSignaling(ctx, *signalAddr, *token)
	case "relay":
		runRelay(ctx, *signalAddr, logf)
		return
	}
	if err != nil {
		log.Fatalf("[wtpunch] %s signaling: %v", *signalMode, err)
	}

	cfg := punch.DefaultConfig()
	if *stun != "" {
		cfg.STUNServers = splitNonEmpty(*stun, ",")
	}
	cfg.PunchTimeout = *timeout
	cfg.Port = *port
	cfg.Log = logf

	if *role == "answer" {
		runAnswer(ctx, cfg, sig, logf)
		return
	}
	runOffer(ctx, cfg, sig, *count, logf)
}

func runAnswer(ctx context.Context, cfg punch.Config, sig punch.Signaling, logf func(string, ...any)) {
	session, err := punch.Respond(ctx, cfg, sig)
	if err != nil {
		logf("punch failed: %v", err)
		logf("fallback: carrier egress (hub as TURN) remains the active path")
		os.Exit(2)
	}
	defer session.Close()
	logf("punched, peer=%s — echoing pings", session.RemoteAddr())
	for {
		payload, err := session.Recv()
		if err != nil {
			logf("recv: %v", err)
			return
		}
		if err := session.Send(payload); err != nil {
			logf("send: %v", err)
			return
		}
	}
}

func runOffer(ctx context.Context, cfg punch.Config, sig punch.Signaling, count int, logf func(string, ...any)) {
	session, err := punch.Initiate(ctx, cfg, sig)
	if err != nil {
		logf("punch failed: %v", err)
		logf("fallback: carrier egress (hub as TURN) remains the active path")
		os.Exit(2)
	}
	defer session.Close()
	logf("punched, peer=%s", session.RemoteAddr())

	var total, min, max time.Duration
	for i := 0; i < count; i++ {
		payload := fmt.Appendf(nil, "ping-%d-%d", i, time.Now().UnixNano())
		start := time.Now()
		if err := session.Send(payload); err != nil {
			logf("send: %v", err)
			os.Exit(2)
		}
		echo, err := session.Recv()
		if err != nil {
			logf("recv: %v", err)
			os.Exit(2)
		}
		if string(echo) != string(payload) {
			logf("echo mismatch on ping %d: %q", i, echo)
			os.Exit(2)
		}
		rtt := time.Since(start)
		if i == 0 || rtt < min {
			min = rtt
		}
		if rtt > max {
			max = rtt
		}
		total += rtt
		logf("ping %d: rtt=%s remote=%s", i, rtt.Round(time.Microsecond), session.RemoteAddr())
		time.Sleep(200 * time.Millisecond)
	}
	logf("done: n=%d avg=%s min=%s max=%s path=direct-udp", count, (total / time.Duration(count)).Round(time.Microsecond), min.Round(time.Microsecond), max.Round(time.Microsecond))
}

func splitNonEmpty(s, sep string) []string {
	var out []string
	for _, part := range strings.Split(s, sep) {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}
