package galleton

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Microck/wallapop-cli/internal/filelock"
	"github.com/Microck/wallapop-cli/internal/galleton/bundle"
)

const hostCommand = "__galleton-host"
const idleGrace = 10 * time.Second

var errHostVersionMismatch = errors.New("a different session engine version is still running; run `wallapop auth service disable && wallapop auth service enable`, or stop watch/MCP processes, then retry")

// errHostProxyMismatch fails closed: a running engine renews through the proxy
// it was started with, so a command asking for another one must not use it.
var errHostProxyMismatch = errors.New("the session engine is running with a different proxy setting; stop watch/MCP processes and run `wallapop auth service stop` (disable the auth service first if it is enabled), or use the same WALLAPOP_PROXY/config proxy, then retry")

type endpoint struct {
	URL     string `json:"url"`
	Version string `json:"version"`
	// Proxy is a keyed fingerprint of the engine's proxy URL, empty for a
	// direct connection. The URL itself, which may carry credentials, is
	// never written to disk.
	Proxy string `json:"proxy,omitempty"`
}

func hostMismatch(err error) bool {
	return errors.Is(err, errHostVersionMismatch) || errors.Is(err, errHostProxyMismatch)
}

// Open obtains a lease on the shared, automatically managed daemon. Releasing
// the last lease allows the host to shut down after a short idle grace period.
// Only read-only readiness probes are retried; imports/renewals are never replayed.
// proxy is the validated egress proxy URL, or empty for a direct connection;
// an engine running with any other setting is refused, never reused.
func Open(ctx context.Context, dir, proxy string) (*Client, error) {
	if Configured() {
		return openExternal(ctx, proxy)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := privateDir(dir); err != nil {
		return nil, err
	}
	lock, err := filelock.Acquire(ctx, filepath.Join(dir, "startup.lock"))
	if err != nil {
		return nil, err
	}
	defer filelock.Release(lock)
	leases := filepath.Join(dir, "leases")
	if err := privateDir(leases); err != nil {
		return nil, err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, err
	}
	leasePath := filepath.Join(leases, hex.EncodeToString(id[:])+".lease")
	lease, err := filelock.Acquire(ctx, leasePath)
	if err != nil {
		return nil, err
	}
	release := func() { filelock.Release(lease); os.Remove(leasePath) }
	success := false
	defer func() {
		if !success {
			release()
		}
	}()
	connect := func() (*Client, error) {
		c, err := running(ctx, dir, proxy)
		if err == nil {
			c.release = release
			success = true
		}
		return c, err
	}
	c, connectErr := connect()
	if connectErr == nil {
		return c, nil
	}
	host, free, err := filelock.Try(filepath.Join(dir, "host.lock"))
	if err != nil {
		return nil, err
	}
	if !free && hostMismatch(connectErr) {
		return nil, connectErr
	}
	if free {
		filelock.Release(host)
		// Prepare before launching so source-build errors are returned to the user.
		if _, err := engineBinary(ctx, dir); err != nil {
			return nil, err
		}
		if err := installAdapter(dir, proxy != ""); err != nil {
			return nil, err
		}
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		exe, err = filepath.EvalSymlinks(exe)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command(exe, hostCommand, dir)
		cmd.Env = hostEnv(proxy)
		detach(cmd)
		// Never inherit terminal pipes: the short-lived CLI must be free to exit.
		if err := cmd.Start(); err != nil {
			return nil, errors.New("could not start the bundled session host")
		}
		go func() { _ = cmd.Wait() }()
	}
	deadline := time.NewTimer(20 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if c, err := connect(); err == nil {
			return c, nil
		} else if !free && hostMismatch(err) {
			// A host that was still starting may have only just published its
			// endpoint. Confirm it is alive before reporting the conflict.
			// Ignore stale endpoints when we launched the replacement ourselves.
			host, unlocked, lockErr := filelock.Try(filepath.Join(dir, "host.lock"))
			if lockErr != nil {
				return nil, lockErr
			}
			if !unlocked {
				return nil, err
			}
			filelock.Release(host)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("session host did not become ready; run `wallapop auth service status` (no credentials were discarded)")
		case <-tick.C:
		}
	}
}

// running connects to the published engine. On a proxy mismatch it returns
// the live client with errHostProxyMismatch, so status can still inspect it;
// callers that act on credentials must treat that as a failure.
func running(ctx context.Context, dir, proxy string) (*Client, error) {
	raw, err := readRegular(filepath.Join(dir, "endpoint.json"), 4096)
	if err != nil {
		return nil, err
	}
	var ep endpoint
	if json.Unmarshal(raw, &ep) != nil {
		return nil, errors.New("invalid session host endpoint")
	}
	if ep.Version != bundle.Version {
		return nil, errHostVersionMismatch
	}
	token, err := readRegular(filepath.Join(dir, "api.token"), 4096)
	if err != nil {
		return nil, err
	}
	c, err := New(ep.URL, string(token))
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, ep.URL+"/v1/health", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	httpClient := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, errors.New("session host is not responding")
	}
	defer resp.Body.Close()
	var health struct {
		OK      bool   `json:"ok"`
		Version string `json:"version"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&health) != nil || !health.OK || health.Version != bundle.EngineVersion {
		return nil, errors.New("session host failed authenticated readiness check")
	}
	if ep.Proxy != proxyFingerprint(token, proxy) {
		return c, errHostProxyMismatch
	}
	return c, nil
}

// HandleHost is called before argument parsing in the executable. It is not a
// public CLI command and never installs a service or handles user credentials.
func HandleHost(args []string) (bool, error) {
	if len(args) == 0 || args[0] != hostCommand {
		return false, nil
	}
	if len(args) != 2 {
		return true, errors.New("invalid session host invocation")
	}
	return true, RunHost(args[1])
}

// RunHost owns one daemon per state directory. A vault lock inside Galleton is
// a second safeguard against duplicate credential rotation across processes.
func RunHost(dir string) error {
	if err := privateDir(dir); err != nil {
		return err
	}
	host, ok, err := filelock.Try(filepath.Join(dir, "host.lock"))
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	defer filelock.Release(host)
	ctx, stop := signal.NotifyContext(context.Background(), hostSignals()...)
	defer stop()
	binary, err := engineBinary(ctx, dir)
	if err != nil {
		return err
	}
	init := exec.CommandContext(ctx, binary, "init", "--dir", dir)
	init.Env = cleanEnv()
	noWindow(init)
	if err := init.Run(); err != nil {
		return errors.New("could not initialize private session storage")
	}
	// The launcher wrote adapters.json for this proxy setting under the
	// startup lock. Refuse to serve one that does not match the environment,
	// so the engine never renews outside the proxy it advertises.
	proxy := os.Getenv(ProxyEnv)
	if raw, err := readRegular(filepath.Join(dir, "adapters.json"), 1<<20); err != nil || string(raw) != string(adapterFor(proxy != "")) {
		return errors.New("session adapter does not match the host's proxy setting")
	}
	cmd := exec.Command(binary, "serve", "--dir", dir, "--config", filepath.Join(dir, "adapters.json"), "--listen", "127.0.0.1:0")
	cmd.Env = hostEnv(proxy)
	noWindow(cmd)
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return errors.New("could not start the session engine")
	}
	done := make(chan error, 1)
	ready := make(chan string, 1)
	// The engine reports its actual bound address. Never reserve-and-release a
	// port or send the API token to a guessed listener.
	go func() {
		sc := bufio.NewScanner(pipe)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "Galleton listening on ") {
				fields := strings.Fields(line)
				if len(fields) > 3 {
					host, port, err := net.SplitHostPort(fields[3])
					p, _ := strconv.Atoi(port)
					if err == nil && host == "127.0.0.1" && p > 0 && p <= 65535 {
						select {
						case ready <- "http://" + fields[3]:
						default:
						}
					}
				}
			}
		}
	}()
	go func() { done <- cmd.Wait() }()
	var c *Client
	var shutdownGuard *os.File
	defer func() {
		// Do not interrupt a pending rotation immediately. The daemon drains its
		// scheduler and checkpoints on authenticated shutdown.
		if c != nil {
			shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			_ = c.Shutdown(shutdown)
			cancel()
		}
		select {
		case <-done:
		case <-time.After(5 * time.Minute):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = os.Remove(filepath.Join(dir, "endpoint.json"))
		filelock.Release(shutdownGuard)
	}()
	select {
	case base := <-ready:
		token, err := readRegular(filepath.Join(dir, "api.token"), 4096)
		if err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		c, err = New(base, string(token))
		if err != nil {
			_ = cmd.Process.Kill()
			return err
		}
		raw, _ := json.Marshal(endpoint{URL: base, Version: bundle.Version, Proxy: proxyFingerprint(token, proxy)})
		if err := atomicFile(filepath.Join(dir, "endpoint.json"), raw, 0600); err != nil {
			return err
		}
		// Also validates the token, URL and health response before accepting leases.
		_, err = running(ctx, dir, proxy)
		if err != nil {
			return err
		}
	case err := <-done:
		// Leave the completion available to the deferred drain.
		done <- err
		return errors.New("session engine stopped before becoming ready")
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		return ctx.Err()
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		return errors.New("session engine startup timed out")
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	lastActive := time.Now()
	for {
		select {
		case err := <-done:
			done <- err
			return errors.New("session engine stopped unexpectedly; retrying a command will inspect its saved checkpoints")
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Serializes the last-client check with new clients acquiring their leases.
			lock, ok, err := filelock.Try(filepath.Join(dir, "startup.lock"))
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			active, err := activeLeases(filepath.Join(dir, "leases"))
			if active {
				lastActive = time.Now()
			}
			if err == nil && !active && time.Since(lastActive) >= idleGrace {
				// Hold this lock until shutdown has drained. New clients cannot connect
				// to an instance halfway through shutdown.
				shutdownGuard = lock
				return nil
			}
			filelock.Release(lock)
			if err != nil {
				return err
			}
		}
	}
}

func activeLeases(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	active := false
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".lease") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		f, free, err := filelock.Try(path)
		if err != nil {
			return false, err
		}
		if !free {
			active = true
			continue
		}
		filelock.Release(f)
		_ = os.Remove(path)
	}
	return active, nil
}

// RuntimeStatus is metadata only: checking it does not start a daemon. A
// running engine also reports, from the engine itself, whether the wallapop
// provider is proxied and until when it is paused.
func RuntimeStatus(ctx context.Context, dir, proxy string) map[string]any {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if Configured() {
		st := map[string]any{"running": false, "external": true}
		if c, err := FromEnv(); err == nil {
			providerStatus(probe, c, st)
		}
		return st
	}
	st := map[string]any{"running": false, "state_dir": dir, "engine_version": bundle.Version, "external": false}
	c, err := running(probe, dir, proxy)
	if errors.Is(err, errHostProxyMismatch) {
		st["proxy_mismatch"] = true
	}
	if c != nil {
		providerStatus(probe, c, st)
	}
	return st
}

// providerStatus fills running, proxied and paused_until. A daemon that
// answers but does not list the provider still counts as running.
func providerStatus(ctx context.Context, c *Client, st map[string]any) {
	p, err := c.provider(ctx)
	if err != nil {
		if IsStatus(err, http.StatusNotFound) {
			st["running"] = true
		}
		return
	}
	st["running"] = true
	st["proxied"] = p.Proxied
	if p.PausedUntil != nil && p.PausedUntil.After(time.Now()) {
		st["paused_until"] = p.PausedUntil.UTC()
	}
}

func Stop(ctx context.Context, dir string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	lock, err := filelock.Acquire(ctx, filepath.Join(dir, "startup.lock"))
	if err != nil {
		return err
	}
	defer filelock.Release(lock)
	active, err := activeLeases(filepath.Join(dir, "leases"))
	if err != nil {
		return err
	}
	if active {
		return fmt.Errorf("session engine is in use; stop the auth service, watch, or MCP process first")
	}
	// Stopping is how a proxy mismatch is resolved, so stop whichever proxy
	// the engine runs with.
	c, err := running(ctx, dir, "")
	if c == nil || (err != nil && !errors.Is(err, errHostProxyMismatch)) {
		return nil
	}
	if err := c.Shutdown(ctx); err != nil {
		return err
	}
	// Keep startup serialized until the old host has fully drained and released
	// its vault. A successful shutdown acknowledgement alone is not completion.
	host, err := filelock.Acquire(ctx, filepath.Join(dir, "host.lock"))
	if err != nil {
		return err
	}
	defer filelock.Release(host)
	vault, err := filelock.Acquire(ctx, filepath.Join(dir, "daemon.lock"))
	if err == nil {
		filelock.Release(vault)
	}
	return err
}
