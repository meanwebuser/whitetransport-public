package carriers

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/meanwebuser/whitetransport/core/internal/fabric"
	"github.com/meanwebuser/whitetransport/core/internal/provider"
)

// ProviderCarrier wraps a provider.Provider into the Carrier interface.
// It is the bridge between the platform plugin layer (Provider) and the
// runtime transport layer (Carrier).
type ProviderCarrier struct {
	prov     provider.Provider
	endpoint Endpoint
	desc     Descriptor
}

type onDataSetter interface {
	SetOnData(func([]byte))
}

// NewProviderCarrier creates a Carrier from a platform Provider.
func NewProviderCarrier(prov provider.Provider, endpoint Endpoint) (*ProviderCarrier, error) {
	desc, err := descriptorFromProvider(prov)
	if err != nil {
		return nil, err
	}
	pc := &ProviderCarrier{
		prov:     prov,
		endpoint: endpoint,
		desc:     desc,
	}
	if setter, ok := prov.(onDataSetter); ok {
		setter.SetOnData(func(data []byte) {
			_ = data
		})
	}
	return pc, nil
}

func (pc *ProviderCarrier) Descriptor() Descriptor {
	return pc.desc
}

// GetProvider returns the underlying platform provider.
func (pc *ProviderCarrier) GetProvider() provider.Provider {
	return pc.prov
}

func (pc *ProviderCarrier) Write(ctx context.Context, endpoint Endpoint, envelope fabric.Envelope) error {
	data, err := MarshalEnvelope(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}
	return pc.prov.Send(ctx, data)
}

func (pc *ProviderCarrier) Read(ctx context.Context, endpoint Endpoint, cursor Cursor) (ReadResult, error) {
	data, err := pc.prov.Receive(ctx)
	if err != nil {
		return ReadResult{}, err
	}
	log.Printf("[provider-carrier] read provider=%s endpoint=%s bytes=%d", pc.desc.ID, endpoint.Address, len(data))
	if len(data) == 0 {
		return ReadResult{Cursor: cursor}, nil
	}
	env, err := ParseEnvelope(data)
	if err != nil {
		log.Printf("[provider-carrier] parse envelope failed provider=%s endpoint=%s err=%v", pc.desc.ID, endpoint.Address, err)
		return ReadResult{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	log.Printf("[provider-carrier] read envelope provider=%s id=%s type=%s source=%s payload=%d", pc.desc.ID, env.ID, env.PayloadType, env.Source, len(env.Payload))
	return ReadResult{
		Envelopes: []fabric.Envelope{env},
		Cursor:    cursor,
	}, nil
}

func (pc *ProviderCarrier) Probe(ctx context.Context, endpoint Endpoint) (Metrics, error) {
	h := pc.prov.Health()
	started := time.Now()
	if err := pc.prov.Send(ctx, []byte{}); err != nil {
		return Metrics{Healthy: false, FailureReason: err.Error()}, err
	}
	return Metrics{
		Healthy:      h.ErrorCount == 0,
		Latency:      time.Since(started),
		LastOK:       time.Now().UTC(),
		BandwidthBPS: 0,
	}, nil
}

func (pc *ProviderCarrier) DeleteMessage(ctx context.Context, endpoint Endpoint, messageID string) error {
	// Provider carriers generally don't support message deletion
	return fmt.Errorf("delete message not implemented for provider carrier: %s", pc.desc.ID)
}

// Start satisfies LifecycleCarrier by forwarding to the provider's Load().
func (pc *ProviderCarrier) Start(ctx context.Context, endpoint Endpoint) error {
	return pc.prov.Load()
}

// Stop satisfies LifecycleCarrier by forwarding to the provider's Unload().
func (pc *ProviderCarrier) Stop(ctx context.Context, endpoint Endpoint) error {
	return pc.prov.Unload()
}

// Health satisfies LifecycleCarrier by translating provider.Health into HealthStatus.
func (pc *ProviderCarrier) Health(ctx context.Context) HealthStatus {
	h := pc.prov.Health()
	return HealthStatus{
		Healthy:     h.ErrorCount == 0,
		Ready:       h.ErrorCount == 0,
		Message:     fmt.Sprintf("errors=%d", h.ErrorCount),
		LastChecked: time.Now().UTC(),
	}
}

func descriptorFromProvider(prov provider.Provider) (Descriptor, error) {
	limits := prov.GetLimits()
	mode := DeliveryMailbox
	switch prov.Category() {
	case provider.CategoryVideo, provider.CategoryAudio:
		mode = DeliveryStream
	case provider.CategoryCloud:
		mode = DeliveryBulk
	}

	trafficClasses := []fabric.TrafficClass{fabric.TrafficControl}
	if mode == DeliveryStream {
		trafficClasses = append(trafficClasses, fabric.TrafficStream, fabric.TrafficEgress)
	} else if mode == DeliveryBulk {
		trafficClasses = append(trafficClasses, fabric.TrafficBulk, fabric.TrafficRepair)
	}

	caps := []Capability{CapRendezvous, CapMailbox, CapRetained}
	if mode == DeliveryStream {
		caps = append(caps, CapStream, CapDuplex)
	} else if mode == DeliveryBulk {
		caps = append(caps, CapBulk)
	}

	return Descriptor{
		ID:             prov.ID(),
		Provider:       prov.ID(),
		Mode:           mode,
		TrafficClasses: trafficClasses,
		Capabilities:   caps,
		Limits: Limits{
			MaxPayloadBytes:   limits.MaxPayloadBytes,
			ChunkPayloadBytes: limits.MaxPayloadBytes,
			SendsPerMinute:    limits.MaxRatePerMinute,
			PollsPerMinute:    limits.MaxRatePerMinute,
			DailyBytes:        limits.MaxDailyBytes,
		},
		Metrics: Metrics{
			Healthy:         true,
			Latency:         200 * time.Millisecond,
			QuotaRemaining:  -1, // providers do not report quota; treat as unlimited
			Reliability:     1.0,
			BandwidthBPS:    0,
		},
		Notes: "Adapted from provider " + prov.ID(),
	}, nil
}
