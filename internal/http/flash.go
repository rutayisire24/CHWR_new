package http

import (
	"net/http"
	"net/url"
	"strings"
)

// flash is a one-shot message carried across a redirect. It lives in a cookie
// rather than the session row: it is display state, and a message lost to a
// closed tab costs nothing.
type flash struct {
	Kind    string // ok | warn | error
	Message string
}

const flashCookie = "chwr_flash"

func setFlash(w http.ResponseWriter, secure bool, kind, message string) {
	http.SetCookie(w, &http.Cookie{
		Name:     flashCookie,
		Value:    url.QueryEscape(kind + "|" + message),
		Path:     "/",
		MaxAge:   60,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// takeFlash reads the flash and expires the cookie in the same response, so a
// message shows once and not on the next page.
func takeFlash(w http.ResponseWriter, r *http.Request) *flash {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})

	decoded, err := url.QueryUnescape(c.Value)
	if err != nil {
		return nil
	}
	kind, message, found := strings.Cut(decoded, "|")
	if !found || message == "" {
		return nil
	}
	switch kind {
	case "ok", "warn", "error":
	default:
		kind = "ok"
	}
	return &flash{Kind: kind, Message: message}
}
