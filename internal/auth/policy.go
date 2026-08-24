package auth

import (
	"strings"
	"unicode"
)

// MinPasswordLength is the floor for admin-set and self-chosen passwords.
// Length carries the strength here; composition rules are deliberately mild,
// because complexity theatre pushes staff toward predictable substitutions.
const MinPasswordLength = 12

// CheckPassword returns a human-readable reason the password is unacceptable,
// or "" when it passes.
func CheckPassword(pw string) string {
	if len([]rune(pw)) < MinPasswordLength {
		return "Password must be at least 12 characters."
	}
	var letter, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			letter = true
		default:
			other = true
		}
	}
	if !letter || !other {
		return "Password must mix letters with at least one digit or symbol."
	}
	if strings.TrimSpace(pw) == "" {
		return "Password cannot be only whitespace."
	}
	return ""
}
