package mobile

import "github.com/xjasonlyu/tun2socks/v2/engine"

// engineRunner adapts the pinned production tun2socks engine to the mobile
// lifecycle without hiding initialization or teardown failures.
type engineRunner struct{}

// Insert installs the next engine configuration.
func (engineRunner) Insert(proxy string, device string, mtu int, logLevel string) {
	engine.Insert(&engine.Key{
		Proxy:    proxy,
		Device:   device,
		MTU:      mtu,
		LogLevel: logLevel,
	})
}

// Start initializes the engine and preserves its production error.
func (engineRunner) Start() error {
	return engine.Start()
}

// Stop tears down the engine and preserves its production error.
func (engineRunner) Stop() error {
	return engine.Stop()
}
