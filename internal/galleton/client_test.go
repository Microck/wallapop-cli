package galleton

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConnection(t *testing.T) {
	for _, base := range []string{
		"https://127.0.0.1:8766", "http://localhost:8766", "http://example.com",
		"http://127.0.0.1/x", "http://127.0.0.1?", "http://127.0.0.1?x=y",
		"http://127.0.0.1/#fragment", "http://user:pass@127.0.0.1", "http://192.168.1.1",
	} {
		t.Run(base, func(t *testing.T) {
			if _, err := New(base, "secret"); err == nil {
				t.Fatal("accepted unsafe daemon URL")
			}
		})
	}
	for _, token := range []string{"", "  ", "one\ntwo", "one\rtwo"} {
		if _, err := New(DefaultURL, token); err == nil {
			t.Fatal("accepted invalid API token")
		}
	}
	for _, base := range []string{"", "http://127.0.0.1:8766/", "http://[::1]:8766"} {
		if _, err := New(base, "secret\n"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSessionIDValidation(t *testing.T) {
	for _, id := range []string{"", "../another", "a/b", "a?b", "a%2fb", "你好", strings.Repeat("a", 65)} {
		if _, err := sessionPath(id); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	if _, err := sessionPath("wallapop-profile_1"); err != nil {
		t.Fatal(err)
	}
}

func TestAPIContract(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		if r.Header.Get("Authorization") != "Bearer local-secret" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("missing local API authentication or content type")
		}
		switch {
		case r.Method == http.MethodPut:
			var in Credentials
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if in.Provider != "wallapop" || in.CookieOrigin != "https://es.wallapop.com" || in.CookieHeader != "session=secret" || !in.Replace || in.ExpectedRevision == nil || *in.ExpectedRevision != 7 {
				t.Errorf("unexpected import: %+v", in)
			}
			w.WriteHeader(http.StatusCreated)
		case strings.HasSuffix(r.URL.Path, "/headers"):
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in["url"] != "https://api.wallapop.com" {
				t.Error("wrong headers target")
			}
			fmt.Fprint(w, `{"headers":{"Authorization":"Bearer access-token"}}`)
			return
		}
		fmt.Fprint(w, `{"id":"test","provider":"wallapop","revision":8,"status":"ready"}`)
	}))
	defer srv.Close()
	c, err := New(srv.URL, "local-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	rev := uint64(7)
	meta, err := c.Connect(ctx, "test", Credentials{Provider: "wallapop", CookieOrigin: "https://es.wallapop.com", CookieHeader: "session=secret", Replace: true, ExpectedRevision: &rev})
	if err != nil || meta.Revision != 8 {
		t.Fatalf("connect: %+v, %v", meta, err)
	}
	if _, err := c.Status(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	h, err := c.Headers(ctx, "test", "https://api.wallapop.com")
	if err != nil || h.Headers["Authorization"] != "Bearer access-token" {
		t.Fatalf("headers: %+v, %v", h, err)
	}
	if _, err := c.Refresh(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if err := c.Forget(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	want := "PUT /v1/sessions/test,GET /v1/sessions/test,POST /v1/sessions/test/headers,POST /v1/sessions/test/refresh,DELETE /v1/sessions/test"
	if strings.Join(calls, ",") != want {
		t.Fatalf("calls = %v", calls)
	}
}

func TestRedirectDoesNotLeakToken(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer destination.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "local-secret")
	_, err := c.Status(context.Background(), "test")
	if !IsStatus(err, http.StatusTemporaryRedirect) || called {
		t.Fatalf("redirect followed or not reported: %v, called=%v", err, called)
	}
	transport := c.http.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Fatal("local administrator token must not use environment proxies")
	}
}

func TestErrorsAreBoundedAndRedacted(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"daemon", 409, `{"error":{"code":"renewal_uncertain","message":"cookie-secret"}}`},
		{"malformed", 200, `not-json cookie-secret`},
		{"oversized", 200, strings.Repeat("x", (1<<20)+1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			c, _ := New(srv.URL, "local-secret")
			_, err := c.Status(context.Background(), "test")
			if err == nil || strings.Contains(err.Error(), "secret") || len(err.Error()) > 200 {
				t.Fatalf("unsafe error: %v", err)
			}
			if tc.status == 409 {
				var apiErr *Error
				if !errors.As(err, &apiErr) || apiErr.Code != "renewal_uncertain" {
					t.Fatalf("lost error classification: %v", err)
				}
			}
		})
	}
}

func TestCancellationAndMissingSession(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error":{"code":"not_found"}}`)
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "secret")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Status(ctx, "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
	if err := c.Forget(context.Background(), "missing"); err != nil {
		t.Fatalf("forget should be idempotent: %v", err)
	}
}

func TestFromEnv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "api.token")
	if err := os.WriteFile(file, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WALLAPOP_GALLETON_DIR", dir)
	t.Setenv("WALLAPOP_GALLETON_URL", "")
	t.Setenv("WALLAPOP_GALLETON_TOKEN_FILE", "")
	c, err := FromEnv()
	if err != nil || c.token != "file-secret" || !Configured() {
		t.Fatalf("from dir: %v", err)
	}
	t.Setenv("WALLAPOP_GALLETON_DIR", filepath.Join(dir, "missing"))
	t.Setenv("WALLAPOP_GALLETON_TOKEN_FILE", file)
	if _, err := FromEnv(); err != nil {
		t.Fatal("explicit token file must take precedence:", err)
	}
	t.Setenv("WALLAPOP_GALLETON_TOKEN_FILE", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("missing token file accepted")
	}
}
