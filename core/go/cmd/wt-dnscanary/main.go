// Command wt-dnscanary runs a live dns.txt carrier canary through a real
// recursive resolver (Yandex 77.88.8.8 by default). It probes the tunnel
// hello, then round-trips one envelope through the resolver, proving the
// full client → resolver → recursion → authoritative exit path.
//
// Usage:
//
//	wt-dnscanary -zone wt.example.com [-resolver 77.88.8.8:53] [-mailbox box-1]
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/fabric"
)

func main() {
	zone := flag.String("zone", "", "delegated tunnel zone, e.g. wt.example.com (required)")
	resolver := flag.String("resolver", "77.88.8.8:53", "recursive resolver used by the client")
	mailbox := flag.String("mailbox", "canary", "mailbox name for the roundtrip")
	timeout := flag.Duration("timeout", 15*time.Second, "overall canary timeout")
	flag.Parse()

	if *zone == "" {
		log.Fatal("wt-dnscanary: -zone is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	carrier, err := carriers.NewDNSTXTCarrier(carriers.DNSTXTConfig{
		Zone:         *zone,
		ResolverAddr: *resolver,
	})
	if err != nil {
		log.Fatalf("wt-dnscanary: %v", err)
	}
	endpoint := carriers.Endpoint{ID: "dnscanary", Address: *mailbox}

	start := time.Now()
	metrics, err := carrier.Probe(ctx, endpoint)
	if err != nil {
		log.Fatalf("wt-dnscanary: probe via %s failed: %v (is the zone delegated and the exit reachable?)", *resolver, err)
	}
	fmt.Printf("DNSCANARY_PROBE_OK latency=%s\n", metrics.Latency.Round(time.Millisecond))

	sent := fabric.NewEnvelope("dnscanary-"+time.Now().Format("150405"), fabric.TrafficHealth, "health.canary",
		[]byte(fmt.Sprintf("dns-txt live canary %s", time.Now().UTC().Round(time.Second))))
	if err := carrier.Write(ctx, endpoint, sent); err != nil {
		log.Fatalf("wt-dnscanary: write: %v", err)
	}

	result, err := carrier.Read(ctx, endpoint, "")
	if err != nil {
		log.Fatalf("wt-dnscanary: read: %v", err)
	}
	for _, env := range result.Envelopes {
		if env.ID == sent.ID && bytes.Equal(env.Payload, sent.Payload) {
			fmt.Printf("DNSCANARY_ROUNDTRIP_OK zone=%s resolver=%s rtt=%s\n",
				*zone, *resolver, time.Since(start).Round(time.Millisecond))
			return
		}
	}
	log.Fatalf("wt-dnscanary: roundtrip envelope not found (%d envelopes)", len(result.Envelopes))
}
