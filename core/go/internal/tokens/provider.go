package tokens

// TokenProvider is the interface that carriers use to resolve tokens and
// report usage. It is satisfied by *Store.
type TokenProvider interface {
	// Resolve finds the best token(s) for a (platform, connectionType,
	// channelID) triple. Returns an ordered list: primary first,
	// fallbacks after. Returns an error if no active token is found.
	Resolve(platform, connectionType, channelID string) ([]*Token, error)

	// RecordUsage increments usage counters for a token after an API call.
	// sent and recv are message counts (or byte counts for bulk carriers).
	// requestErr should be non-nil if the API call failed.
	RecordUsage(tokenID, connectionType, channelID string, sent, recv int64, requestErr error)
}

// Verify *Store implements TokenProvider at compile time.
var _ TokenProvider = (*Store)(nil)

// TokenHealthChecker allows the policy engine and carrier health tracker to
// gate carriers on token health. Implemented by *Store; nil means no check.
type TokenHealthChecker interface {
	IsCarrierHealthy(carrierID string) bool
}

// Verify *Store implements TokenHealthChecker at compile time.
var _ TokenHealthChecker = (*Store)(nil)

// NoopTokenProvider is a TokenProvider that does nothing, for testing or
// when no token store is configured.
type NoopTokenProvider struct{}

func (NoopTokenProvider) Resolve(_, _, _ string) ([]*Token, error) {
	return nil, nil
}

func (NoopTokenProvider) RecordUsage(_, _, _ string, _, _ int64, _ error) {}

var _ TokenProvider = NoopTokenProvider{}
