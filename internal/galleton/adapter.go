package galleton

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ProxyEnv carries the egress proxy URL from the CLI to the session host and
// from the host to the engine, whose adapter names it in proxy_env. The URL
// may hold credentials, so it travels only through the environment.
const ProxyEnv = "WALLAPOP_PROXY"

//go:embed adapter.json
var adapter []byte

// legacyAdapterSHA256 identifies the adapter that releases up to v0.3.2
// installed. Its credential fields equal the current ones; the current adapter
// only adds block_signatures, min_interval_ms and the optional proxy_env, which
// Galleton leaves out of its configuration hash, so sessions imported under it
// keep working and the file is replaced on the next engine start.
const legacyAdapterSHA256 = "f6616b3b935c4fc191c4a2246f31dd94a427fdbbb2a313209dc9519f493e9287"

// adapterFor renders the bundled adapter. With a proxy, the provider reads it
// from ProxyEnv; Galleton then refuses to start when that variable is missing
// and never falls back to a direct connection.
func adapterFor(proxied bool) []byte {
	if !proxied {
		return adapter
	}
	var doc struct {
		Providers []map[string]any `json:"providers"`
	}
	if err := json.Unmarshal(adapter, &doc); err != nil || len(doc.Providers) != 1 {
		panic("bundled adapter.json is invalid")
	}
	doc.Providers[0]["proxy_env"] = ProxyEnv
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(out, '\n')
}

// ownAdapter reports whether raw is an adapter this CLI wrote: the current one
// with or without a proxy, or the one older releases installed. Anything else
// was edited by hand and is never silently replaced.
func ownAdapter(raw []byte) bool {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]) == legacyAdapterSHA256 || bytes.Equal(raw, adapterFor(false)) || bytes.Equal(raw, adapterFor(true))
}

// installAdapter writes the adapter for this proxy setting before the host
// starts. The caller holds the startup lock and has seen the host lock free.
func installAdapter(dir string, proxied bool) error {
	path := filepath.Join(dir, "adapters.json")
	want := adapterFor(proxied)
	old, err := readRegular(path, 1<<20)
	switch {
	case err == nil && bytes.Equal(old, want):
		return nil
	case err == nil && !ownAdapter(old):
		return errors.New("managed session adapter changed; restore the bundled adapter or use an explicit external daemon")
	case err != nil && !errors.Is(err, os.ErrNotExist):
		return err
	}
	return atomicFile(path, want, 0600)
}

func hostEnv(proxy string) []string {
	env := cleanEnv()
	if proxy != "" {
		env = append(env, ProxyEnv+"="+proxy)
	}
	return env
}

// proxyFingerprint lets a client check which proxy a running engine uses
// without the URL, or its credentials, being written to disk. It is keyed with
// the vault's API token, so the fingerprint alone does not reveal the URL.
func proxyFingerprint(token []byte, proxy string) string {
	if proxy == "" {
		return ""
	}
	mac := hmac.New(sha256.New, bytes.TrimSpace(token))
	mac.Write([]byte(proxy))
	return hex.EncodeToString(mac.Sum(nil))
}

// openExternal connects to an operator-run daemon. Its proxy is the
// operator's configuration; when the CLI asks for a proxy, the daemon must at
// least report the wallapop provider as proxied, or nothing is sent through it.
func openExternal(ctx context.Context, proxy string) (*Client, error) {
	c, err := FromEnv()
	if err != nil || proxy == "" {
		return c, err
	}
	p, err := c.provider(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Proxied {
		return nil, errors.New("a proxy is configured, but the external Galleton daemon sends wallapop renewals directly; give its wallapop provider a proxy or proxy_env, or unset WALLAPOP_PROXY and the config proxy")
	}
	return c, nil
}
