package auth

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// csrfHandler wraps a recording handler in the middleware, with the error page
// the router supplies.
func csrfHandler(t *testing.T, reached *bool, seen *http.Request) http.Handler {
	t.Helper()
	pages := ErrorPages{Forbidden: func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}}
	return CSRF(false, pages)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = true
		*seen = *r
	}))
}

// multipartBody builds an upload with a token field and a file part, in that
// order — the same order the form renders them.
func multipartBody(t *testing.T, token, filename, content string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if token != "" {
		if err := w.WriteField(CSRFField, token); err != nil {
			t.Fatalf("write token field: %v", err)
		}
	}
	part, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	return &buf, w.FormDataContentType()
}

// A multipart body is exactly the case ParseForm does not read: before the
// middleware learned to parse one, every file upload was refused with a 403
// because PostForm — and so the token field — came back empty.
func TestCSRFAcceptsMultipartUpload(t *testing.T) {
	const token = "token-from-the-cookie"
	body, contentType := multipartBody(t, token, "chws.csv", "first_name,last_name\nAdongo,Grace\n")

	r := httptest.NewRequest(http.MethodPost, "/imports", body)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: token})

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if !reached {
		t.Fatalf("handler not reached: status %d, body %q", w.Code, w.Body.String())
	}
	// The handler reads the rest of the form the same way it does on any other
	// post, so the parse has to leave PostForm populated, not just the token.
	if got := seen.PostForm.Get(CSRFField); got != token {
		t.Errorf("PostForm token = %q, want %q", got, token)
	}
	if seen.MultipartForm == nil || len(seen.MultipartForm.File["file"]) != 1 {
		t.Fatal("the file part did not survive to the handler")
	}
	if name := seen.MultipartForm.File["file"][0].Filename; name != "chws.csv" {
		t.Errorf("filename = %q, want chws.csv", name)
	}
}

func TestCSRFRefusesMultipartWithoutToken(t *testing.T) {
	body, contentType := multipartBody(t, "", "chws.csv", "anything")

	r := httptest.NewRequest(http.MethodPost, "/imports", body)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: "token-from-the-cookie"})

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if reached {
		t.Fatal("a multipart post with no token reached the handler")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

func TestCSRFRefusesMultipartWithWrongToken(t *testing.T) {
	body, contentType := multipartBody(t, "a-token-of-its-own", "chws.csv", "anything")

	r := httptest.NewRequest(http.MethodPost, "/imports", body)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: "token-from-the-cookie"})

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if reached {
		t.Fatal("a multipart post with a forged token reached the handler")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
}

// The cap is on the whole body, and it is refused as too large rather than as
// malformed: the operator's next move depends on knowing which.
func TestCSRFRefusesOversizedUpload(t *testing.T) {
	const token = "token-from-the-cookie"
	body, contentType := multipartBody(t, token, "huge.csv",
		strings.Repeat("x", MaxMultipartBytes+1))

	r := httptest.NewRequest(http.MethodPost, "/imports", body)
	r.Header.Set("Content-Type", contentType)
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: token})

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if reached {
		t.Fatal("an oversized upload reached the handler")
	}
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want %d", w.Code, http.StatusRequestEntityTooLarge)
	}
}

// The ordinary form post has to keep working exactly as it did.
func TestCSRFStillHandlesURLEncodedForms(t *testing.T) {
	const token = "token-from-the-cookie"

	r := httptest.NewRequest(http.MethodPost, "/chws/new",
		strings.NewReader(CSRFField+"="+token+"&first_name=Grace"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: CSRFCookie, Value: token})

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if !reached {
		t.Fatalf("handler not reached: status %d", w.Code)
	}
	if got := seen.PostForm.Get("first_name"); got != "Grace" {
		t.Errorf("PostForm first_name = %q, want Grace", got)
	}
	if seen.MultipartForm != nil {
		t.Error("a urlencoded post produced a multipart form")
	}
}

// A GET is not mutating, so no body is parsed and no token is demanded — but a
// token still reaches the template, because that is what renders the field.
func TestCSRFIssuesTokenOnGET(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/imports", nil)

	var reached bool
	var seen http.Request
	w := httptest.NewRecorder()
	csrfHandler(t, &reached, &seen).ServeHTTP(w, r)

	if !reached {
		t.Fatal("a GET was refused")
	}
	if CSRFTokenFrom(seen.Context()) == "" {
		t.Error("no token in the request context")
	}
	if len(w.Result().Cookies()) == 0 {
		t.Error("no token cookie issued")
	}
}
