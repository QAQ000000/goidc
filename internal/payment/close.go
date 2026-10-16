package payment

import "errors"

// Close errors are deliberately coarse. Adapters must not expose provider
// response text, URLs, or credentials through the API or audit payload.
var (
	ErrCloseUnavailable = errors.New("payment provider close unavailable")
	ErrCloseUnknown     = errors.New("payment provider close result unknown")
	ErrCloseInvalid     = errors.New("payment provider close result invalid")
)
