package main

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRefreshKeepsTunnelRoutesDuringMetadataAndBypassChanges(t *testing.T) {
	for _, scenario := range []string{"lease", "bypasses", "add-failure", "save-failure", "socks-change", "tunnel-route-change"} {
		t.Run(scenario, func(t *testing.T) {
			cfg := Config{SOCKSHost: "127.0.0.1", SOCKSPort: 12345, Mode: "full", MTU: 1500, DaemonInstanceID: "daemon-a", ProfileRevision: 7, SessionID: "old", BypassCIDRs: []string{"198.51.100.1/32"}}
			current := State{PID: 123, Interface: "utun9", Gateway: "192.0.2.1", Config: cfg, Routes: cfg.RoutePlan().Routes}
			next := cfg
			next.ProfileRevision, next.SessionID, next.ProfileValidUntil = 8, "new", time.Now().Add(time.Minute)
			if scenario != "lease" {
				next.BypassCIDRs = []string{"198.51.100.2/32"}
			}
			if scenario == "socks-change" {
				next.SOCKSPort++
			}
			plan := next.RoutePlan()
			if scenario == "tunnel-route-change" {
				plan.Routes = plan.Routes[1:]
			}
			live := make(map[string]Route)
			for _, route := range current.Routes {
				live[route.CIDR] = route
			}
			var operations []string
			checkTunnel := func() {
				for _, prefix := range []string{"0.0.0.0/1", "128.0.0.0/1"} {
					if live[prefix].Via != "utun" {
						t.Fatal("refresh exposed physical default route")
					}
				}
			}
			saved := current
			got, err := refreshState(current, next, plan,
				func(route Route) error {
					checkTunnel()
					operations = append(operations, "add:"+route.CIDR)
					if scenario == "add-failure" {
						return errors.New("route add failed")
					}
					live[route.CIDR] = route
					return nil
				},
				func(route Route) error {
					operations = append(operations, "remove:"+route.CIDR)
					delete(live, route.CIDR)
					checkTunnel()
					return nil
				},
				func(state State) error {
					checkTunnel()
					if scenario == "save-failure" && state.Config.SessionID == "new" {
						return errors.New("metadata write failed")
					}
					saved = state
					return nil
				},
			)
			wantError := scenario != "lease" && scenario != "bypasses"
			if (err != nil) != wantError {
				t.Fatalf("error=%v, wantError=%t", err, wantError)
			}
			checkTunnel()
			if got.PID != current.PID || got.Interface != current.Interface {
				t.Fatal("refresh replaced owned process/interface")
			}
			if !wantError && saved.Config.SessionID != "new" {
				t.Fatal("current profile metadata was not saved")
			}
			if wantError && saved.Config.SessionID != "old" {
				t.Fatal("failed refresh replaced authorized metadata")
			}
			if scenario == "lease" && len(operations) != 0 {
				t.Fatalf("lease refresh mutated routes: %v", operations)
			}
			if scenario == "bypasses" && !reflect.DeepEqual(operations, []string{"add:198.51.100.2/32", "remove:198.51.100.1/32"}) {
				t.Fatalf("refresh order=%v, want add before remove", operations)
			}
			if wantError {
				if _, exists := live["198.51.100.1/32"]; !exists || len(live) != len(current.Routes) {
					t.Fatalf("failed refresh did not restore physical bypasses: %+v", live)
				}
			}
		})
	}
}
