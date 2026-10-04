package galleton

import (
	"bufio"
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Microck/wallapop-cli/internal/galleton/bundle"
)

// Exercise the pinned executable and official SDK, not a mock daemon. The TLS
// provider accepts only the most recently issued cookie, making lost rotation
// visible after restart. No real Wallapop endpoint is contacted.
func TestRealDaemonPersistsRotationAcrossRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("hermetic SSL_CERT_FILE trust is Linux-only; native lifecycle tests run on every supported OS")
	}
	if len(bundle.Binary) == 0 {
		t.Skip("requires generated bundle")
	}
	var mu sync.Mutex
	cookie := "initial-test-credential"
	calls := 0
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		ck, err := r.Cookie("__Secure-next-auth.session-token")
		if err != nil || ck.Value != cookie {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		calls++
		cookie = fmt.Sprintf("rotated-test-credential-%d", calls)
		http.SetCookie(w, &http.Cookie{Name: ck.Name, Value: cookie, Path: "/", Secure: true, HttpOnly: true, MaxAge: 3600})
		_ = json.NewEncoder(w).Encode(map[string]string{"token": fmt.Sprintf("test-access-%d", calls)})
	}))
	defer provider.Close()
	dir := filepath.Join(t.TempDir(), "vault")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	binary, err := engineBinary(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	cert := filepath.Join(t.TempDir(), "provider.pem")
	if err := os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: provider.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSL_CERT_FILE", cert)
	t.Setenv("SSL_CERT_DIR", t.TempDir())
	cfg := []byte(fmt.Sprintf(`{"providers":[{"name":"wallapop","kind":"http","origins":[%q],"refresh_url":%q,"refresh_method":"GET","refresh_interval_seconds":240,"response":{"access_token_pointer":"/token","require_set_cookie":true}}]}`, provider.URL, provider.URL+"/api/auth/session"))
	configPath := filepath.Join(dir, "test-adapters.json")
	if err := os.WriteFile(configPath, cfg, 0600); err != nil {
		t.Fatal(err)
	}
	start := func() (*Client, func()) {
		t.Helper()
		init := exec.Command(binary, "init", "--dir", dir)
		init.Env = cleanEnv()
		noWindow(init)
		if err := init.Run(); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(binary, "serve", "--dir", dir, "--config", configPath, "--listen", "127.0.0.1:0")
		cmd.Env = cleanEnv()
		noWindow(cmd)
		pipe, err := cmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		address := make(chan string, 1)
		go func() {
			sc := bufio.NewScanner(pipe)
			for sc.Scan() {
				fields := strings.Fields(sc.Text())
				if len(fields) > 3 && strings.HasPrefix(sc.Text(), "Galleton listening on ") {
					address <- "http://" + fields[3]
					break
				}
			}
		}()
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })
		var base string
		select {
		case base = <-address:
		case <-time.After(10 * time.Second):
			t.Fatal("daemon did not start")
		}
		token, err := os.ReadFile(filepath.Join(dir, "api.token"))
		if err != nil {
			t.Fatal(err)
		}
		c, err := New(base, string(token))
		if err != nil {
			t.Fatal(err)
		}
		return c, func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := c.Shutdown(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("daemon did not drain")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, stop := start()
	_, err = c.Connect(ctx, "account", Credentials{Provider: "wallapop", CookieOrigin: provider.URL, CookieHeader: "__Secure-next-auth.session-token=initial-test-credential"})
	if err != nil {
		t.Fatal(err)
	}
	h, err := c.Headers(ctx, "account", provider.URL)
	if err != nil || h.Headers["Authorization"] != "Bearer test-access-1" {
		t.Fatalf("initial renewal: %+v %v", h, err)
	}
	if _, err := c.Headers(ctx, "account", provider.URL); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	count := calls
	mu.Unlock()
	if count != 1 {
		t.Fatalf("cached token caused %d renewals", count)
	}
	stop()
	files, err := filepath.Glob(filepath.Join(dir, "*.session-v2"))
	if err != nil || len(files) != 1 {
		t.Fatalf("vault files: %v %v", files, err)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil || strings.Contains(string(raw), "test-credential") || strings.Contains(string(raw), "test-access") {
		t.Fatal("provider secrets were not encrypted", err)
	}
	c, stop = start()
	defer stop()
	if _, err := c.Refresh(ctx, "account"); err != nil {
		t.Fatal("lost rotated cookie after restart:", err)
	}
	h, err = c.Headers(ctx, "account", provider.URL)
	if err != nil || h.Headers["Authorization"] != "Bearer test-access-2" {
		t.Fatalf("second renewal: %+v %v", h, err)
	}
	if err := c.Forget(ctx, "account"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx, "account"); !IsStatus(err, 404) {
		t.Fatal("session not forgotten", err)
	}
}
