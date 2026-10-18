// Package notify contains transport-neutral notification worker contracts.
package notify

import "time"

type Job struct {
	ID         int64
	LeaseToken string
	Channel    string
	InstanceID string
	Target     string
	Body       string
	Subject    string
	ExpiresAt  time.Time
}

// Accepted means the provider acknowledged delivery, not that a human read it.
// Unknown is terminal until reconciliation; only an explicit rate limit can
// schedule an automatic retry.
type Result struct {
	Outcome           string
	Code              string
	ProviderMessageID string
	RetryAfter        time.Duration
}
