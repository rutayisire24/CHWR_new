package http

import "testing"

// safeNext guards the login form's ?next= against being used as an open
// redirect onto another host.
func TestSafeNext(t *testing.T) {
	cases := map[string]string{
		"":                        "/",
		"/users":                  "/users",
		"/users?role=admin":       "/users?role=admin",
		"https://evil.example/x":  "/",
		"//evil.example/x":        "/",
		"http://127.0.0.1:8099/x": "/",
		"users":                   "/",
		"/../etc/passwd":          "/../etc/passwd", // same-site; the browser resolves it and the mux 404s
	}
	for in, want := range cases {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}
