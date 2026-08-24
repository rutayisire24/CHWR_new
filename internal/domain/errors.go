package domain

import "errors"

// Sentinel errors. Stores return these; handlers map them to status codes and
// messages. Everything else wraps with fmt.Errorf("...: %w", err).
var (
	// ErrNotFound is returned when a lookup finds no row, or finds one the
	// caller's Scope excludes. The two are deliberately indistinguishable: a
	// district user must not learn that a CHW exists in another district.
	ErrNotFound = errors.New("not found")

	// ErrConflict is a uniqueness violation — a duplicate email, or a NIN
	// already on another CHW.
	ErrConflict = errors.New("conflict")

	// ErrInvalidCredentials covers a wrong password, an unknown email and a
	// disabled account alike. Login must not reveal which.
	ErrInvalidCredentials = errors.New("invalid credentials")

	// ErrSessionExpired means the cookie carried a token that is unknown or
	// past its expiry.
	ErrSessionExpired = errors.New("session expired")

	// ErrForbidden means authenticated but not permitted: the role lacks the
	// capability, or the target is outside the user's district.
	ErrForbidden = errors.New("forbidden")
)

// ValidationError carries per-field messages back to a form template.
type ValidationError struct {
	Fields map[string]string
}

// NewValidationError returns an empty error ready to collect field messages.
func NewValidationError() *ValidationError {
	return &ValidationError{Fields: make(map[string]string)}
}

// Add records a message against a field name.
func (e *ValidationError) Add(field, msg string) {
	e.Fields[field] = msg
}

// Any reports whether any field failed.
func (e *ValidationError) Any() bool { return len(e.Fields) > 0 }

// OrNil returns e when it holds messages and nil when it does not, so callers
// can `return v.OrNil()` without a typed-nil trap.
func (e *ValidationError) OrNil() error {
	if e.Any() {
		return e
	}
	return nil
}

func (e *ValidationError) Error() string { return "validation failed" }
