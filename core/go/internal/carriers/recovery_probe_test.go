package carriers

import "testing"

func TestProviderCarrierIsNotSafeEgressRecoveryProber(t *testing.T) {
	var carrier Carrier = (*ProviderCarrier)(nil)
	if _, ok := carrier.(SafeEgressRecoveryProber); ok {
		t.Fatal("ProviderCarrier must not be eligible for background recovery probes")
	}
}
