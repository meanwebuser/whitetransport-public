package mobile

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const (
	tun2SocksProbeProxyEnv  = "WT_TUN2SOCKS_PROBE_PROXY"
	tun2SocksProbeDeviceEnv = "WT_TUN2SOCKS_PROBE_DEVICE"
	tun2SocksProbeMarker    = "production-adapter-returned-error"
)

func TestProductionEngineRunnerReturnsInitializationErrorsWithoutTerminating(t *testing.T) {
	tests := []struct {
		name   string
		proxy  string
		device string
	}{
		{name: "invalid proxy", proxy: "unsupported://127.0.0.1:1", device: "fd://-1"},
		{name: "invalid fd", proxy: "socks5://127.0.0.1:1", device: "fd://-1"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(os.Args[0], "-test.run=^TestProductionEngineRunnerFailureProbe$")
			command.Env = append(os.Environ(), tun2SocksProbeProxyEnv+"="+test.proxy, tun2SocksProbeDeviceEnv+"="+test.device)
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("production adapter terminated the process instead of returning an error: %v\n%s", err, output)
			}
			if !strings.Contains(string(output), tun2SocksProbeMarker) {
				t.Fatalf("production adapter did not return an initialization error:\n%s", output)
			}
		})
	}
}

func TestProductionEngineRunnerFailureProbe(t *testing.T) {
	proxy := os.Getenv(tun2SocksProbeProxyEnv)
	device := os.Getenv(tun2SocksProbeDeviceEnv)
	if proxy == "" || device == "" {
		t.Skip("helper process only")
	}

	runner := engineRunner{}
	runner.Insert(proxy, device, 1500, "silent")
	if err := runner.Start(); err == nil {
		t.Fatal("production adapter returned nil for an invalid engine configuration")
	}
	fmt.Fprintln(os.Stdout, tun2SocksProbeMarker)
}
