//go:build !darwin && !windows

package main

func newSystemVPNHost() systemVPNHost { return unsupportedSystemVPNHost{} }
