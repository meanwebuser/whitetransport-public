package tunnel

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

// physicalBindingKey identifies one provider adapter and physical mailbox.
// Session endpoint IDs are aliases and therefore deliberately excluded when a
// carrier address exists; including them would multiply provider readers for
// the same underlying mailbox.
func physicalBindingKey(binding policy.CarrierBinding) string {
	address := strings.TrimSpace(binding.Endpoint.Address)
	if address == "" {
		address = strings.TrimSpace(binding.Endpoint.ID)
	}
	return strings.Join([]string{carrierInstanceKey(binding.Carrier), address}, "\x00")
}

func carrierInstanceKey(carrier carriers.Carrier) string {
	if carrier == nil {
		return "<nil>"
	}
	value := reflect.ValueOf(carrier)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return fmt.Sprintf("%T@%x", carrier, value.Pointer())
	default:
		descriptor := carrier.Descriptor()
		return fmt.Sprintf("%T:%s:%s", carrier, descriptor.Provider, descriptor.ID)
	}
}

// boundedCarrierPollInterval enforces the advertised provider poll budget and
// a caller-specific floor. A zero provider limit means no stricter budget is
// known, so the floor remains authoritative.
func boundedCarrierPollInterval(binding policy.CarrierBinding, floor time.Duration) time.Duration {
	interval := floor
	pollsPerMinute := binding.Carrier.Descriptor().Limits.PollsPerMinute
	if pollsPerMinute > 0 {
		budgetInterval := time.Minute / time.Duration(pollsPerMinute)
		if budgetInterval > interval {
			interval = budgetInterval
		}
	}
	if interval <= 0 {
		return time.Millisecond
	}
	return interval
}
