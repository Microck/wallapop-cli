package galleton

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// operational lists the adapter fields Galleton leaves out of its
// configuration hash (docs/ADAPTERS.md in v0.1.0). Changing any other field
// moves every imported session to configuration_error.
var operational = []string{"proxy", "proxy_env", "keep_alive", "min_interval_ms", "max_request_body_bytes", "max_response_body_bytes", "block_signatures"}

func providerFields(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var doc struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil || len(doc.Providers) != 1 {
		t.Fatalf("adapter: %v", err)
	}
	return doc.Providers[0]
}

// Sessions imported by v0.3.2 and earlier must keep working after the
// upgrade, so the adapter may only differ from theirs in operational fields.
func TestAdapterKeepsTheCredentialFieldsOfThePreviousRelease(t *testing.T) {
	legacy, err := os.ReadFile(filepath.Join("testdata", "adapter-v0.3.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(legacy); hex.EncodeToString(sum[:]) != legacyAdapterSHA256 {
		t.Fatal("legacyAdapterSHA256 does not identify the v0.3.2 adapter")
	}
	want := providerFields(t, legacy)
	for _, proxied := range []bool{false, true} {
		got := providerFields(t, adapterFor(proxied))
		for _, k := range operational {
			delete(got, k)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("proxied=%v changes hashed adapter fields:\n got %v\nwant %v", proxied, got, want)
		}
	}
	p := providerFields(t, adapterFor(false))
	if p["block_signatures"] == nil || p["min_interval_ms"] == nil || p["proxy_env"] != nil {
		t.Fatalf("bundled adapter options: %v", p)
	}
	if p := providerFields(t, adapterFor(true)); p["proxy_env"] != ProxyEnv || p["proxy"] != nil {
		t.Fatalf("a proxied adapter must name the environment variable, never the URL: %v", p)
	}
}

// An external daemon configured from the example must accept sessions the
// bundled engine would, so the two stay byte-identical.
func TestExternalExampleMatchesTheBundledAdapter(t *testing.T) {
	example, err := os.ReadFile(filepath.Join("..", "..", "examples", "galleton.wallapop.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(example) != string(adapter) {
		t.Fatal("examples/galleton.wallapop.json differs from internal/galleton/adapter.json")
	}
}

func TestInstallAdapterReplacesOnlyItsOwnAdapters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "adapters.json")
	legacy, err := os.ReadFile(filepath.Join("testdata", "adapter-v0.3.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	// A source build from a CRLF checkout embedded and installed a CRLF copy.
	crlf := []byte(strings.ReplaceAll(string(legacy), "\n", "\r\n"))
	for _, step := range []struct {
		old     []byte
		proxied bool
	}{{legacy, false}, {legacy, true}, {crlf, false}, {adapterFor(true), false}, {adapterFor(false), true}} {
		if err := os.WriteFile(path, step.old, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := installAdapter(dir, step.proxied); err != nil {
			t.Fatal(err)
		}
		if got, _ := os.ReadFile(path); string(got) != string(adapterFor(step.proxied)) {
			t.Fatalf("proxied=%v: not replaced", step.proxied)
		}
	}
	edited := []byte(`{"providers":[]}`)
	if err := os.WriteFile(path, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installAdapter(dir, false); err == nil {
		t.Fatal("replaced an operator-modified adapter")
	}
	if got, _ := os.ReadFile(path); string(got) != string(edited) {
		t.Fatal("touched an operator-modified adapter")
	}
}

func TestProxyFingerprintIsKeyedAndHidesTheURL(t *testing.T) {
	const proxy = "socks5h://user:secret@127.0.0.1:1080"
	a := proxyFingerprint([]byte("token-a\n"), proxy)
	if a == "" || strings.Contains(a, "secret") || a != proxyFingerprint([]byte("token-a"), proxy) {
		t.Fatalf("fingerprint %q", a)
	}
	if a == proxyFingerprint([]byte("token-b"), proxy) || a == proxyFingerprint([]byte("token-a"), "socks5h://127.0.0.1:1080") {
		t.Fatal("fingerprint ignores the key or the URL")
	}
	if proxyFingerprint([]byte("token-a"), "") != "" {
		t.Fatal("a direct engine has no fingerprint")
	}
	for _, env := range hostEnv(proxy) {
		if strings.HasPrefix(env, ProxyEnv+"=") && env != ProxyEnv+"="+proxy {
			t.Fatal(env)
		}
	}
}

// The real engine started for one proxy setting is never used for another,
// renews through the proxy it advertises, and keeps a pause across restarts.
func TestBundledEngineHonoursProxySettingAndPersistsPause(t *testing.T) {
	dir := bundledRuntime(t)
	ctx := context.Background()
	const proxy = "socks5h://user:pw@127.0.0.1:1"
	c, err := Open(ctx, dir, proxy)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "adapters.json")); string(raw) != string(adapterFor(true)) {
		t.Fatal("proxied engine did not get the proxied adapter")
	}
	for _, file := range []string{"adapters.json", "endpoint.json"} {
		if raw, _ := os.ReadFile(filepath.Join(dir, file)); strings.Contains(string(raw), "127.0.0.1:1\"") || strings.Contains(string(raw), "pw@") {
			t.Fatalf("%s holds the proxy URL", file)
		}
	}
	if st := RuntimeStatus(ctx, dir, proxy); st["running"] != true || st["proxied"] != true || st["proxy_mismatch"] != nil {
		t.Fatalf("status %v", st)
	}
	if _, err := Open(ctx, dir, ""); !errors.Is(err, errHostProxyMismatch) {
		t.Fatalf("direct client reused a proxied engine: %v", err)
	}
	if _, err := Open(ctx, dir, "socks5h://127.0.0.1:2"); !errors.Is(err, errHostProxyMismatch) {
		t.Fatalf("client reused an engine with another proxy: %v", err)
	}
	if st := RuntimeStatus(ctx, dir, ""); st["proxy_mismatch"] != true || st["running"] != true {
		t.Fatalf("status %v", st)
	}
	until, err := c.Pause(ctx, 600)
	if err != nil || until.Before(time.Now().Add(9*time.Minute)) {
		t.Fatalf("pause: %v %v", until, err)
	}
	c.Close()
	if err := Stop(ctx, dir); err != nil {
		t.Fatal(err)
	}
	c, err = Open(ctx, dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st := RuntimeStatus(ctx, dir, "")
	if st["running"] != true || st["proxied"] != false {
		t.Fatalf("status %v", st)
	}
	if paused, ok := st["paused_until"].(time.Time); !ok || !paused.Equal(until.UTC()) {
		t.Fatalf("pause lost across restart: %v, want %v", st["paused_until"], until)
	}
}

// An engine stopped by an older release left its adapter behind; the next
// start replaces it instead of refusing to run.
func TestBundledEngineUpgradesTheLegacyAdapter(t *testing.T) {
	dir := bundledRuntime(t)
	if err := privateDir(dir); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.ReadFile(filepath.Join("testdata", "adapter-v0.3.2.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "adapters.json"), legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Open(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if raw, _ := os.ReadFile(filepath.Join(dir, "adapters.json")); string(raw) != string(adapter) {
		t.Fatal("legacy adapter not replaced")
	}
}
