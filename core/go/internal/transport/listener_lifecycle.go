package transport

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/meanwebuser/whitetransport/core/internal/carriers"
	"github.com/meanwebuser/whitetransport/core/internal/policy"
)

type listenerCarrierIdentity struct {
	typeName string
	pointer  uintptr
}

// startListenerCarriers starts each distinct listener instance in stable binding order.
func startListenerCarriers(ctx context.Context, bindings map[string]policy.CarrierBinding) ([]carriers.ListenerCarrier, error) {
	keys := make([]string, 0, len(bindings))
	for key := range bindings {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	started := make([]carriers.ListenerCarrier, 0, len(keys))
	seen := make(map[listenerCarrierIdentity]struct{}, len(keys))
	for _, key := range keys {
		binding := bindings[key]
		listener, ok := binding.Carrier.(carriers.ListenerCarrier)
		if !ok {
			continue
		}
		identity, err := listenerIdentity(listener)
		if err != nil {
			unwindErr := stopListenerCarriers(ctx, started)
			return nil, errors.Join(fmt.Errorf("listener binding %s: %w", key, err), unwindErr)
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		if err := listener.StartListener(ctx, binding.Endpoint); err != nil {
			unwindErr := stopListenerCarriers(ctx, started)
			return nil, errors.Join(fmt.Errorf("start listener binding %s: %w", key, err), unwindErr)
		}
		seen[identity] = struct{}{}
		started = append(started, listener)
	}
	return started, nil
}

// stopListenerCarriers stops listener instances in reverse startup order.
func stopListenerCarriers(ctx context.Context, listeners []carriers.ListenerCarrier) error {
	var stopErrors []error
	for index := len(listeners) - 1; index >= 0; index-- {
		listener := listeners[index]
		if listener == nil {
			continue
		}
		if err := listener.StopListener(ctx); err != nil {
			stopErrors = append(stopErrors, fmt.Errorf("stop listener %T: %w", listener, err))
		}
	}
	return errors.Join(stopErrors...)
}

func listenerIdentity(listener carriers.ListenerCarrier) (listenerCarrierIdentity, error) {
	value := reflect.ValueOf(listener)
	if !value.IsValid() || value.Kind() != reflect.Pointer || value.IsNil() {
		return listenerCarrierIdentity{}, fmt.Errorf("listener carrier %T must be a non-nil pointer-backed instance", listener)
	}
	return listenerCarrierIdentity{typeName: value.Type().String(), pointer: value.Pointer()}, nil
}
