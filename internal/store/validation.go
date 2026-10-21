package store

// ValidationError reports invalid input independently of transport protocols.
// State, version, ownership and idempotency conflicts remain Conflict errors.
type ValidationError struct{ Code, Message string }

func (e *ValidationError) Error() string { return e.Message }

func invalidInput(code, message string) error {
	return &ValidationError{Code: code, Message: message}
}
