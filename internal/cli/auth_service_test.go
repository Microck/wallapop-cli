package cli

import (
	"bytes"
	"encoding/xml"
	"strings"
	"testing"
)

func TestAuthServiceDefinitionsQuotePathsAndContainNoSecrets(t *testing.T) {
	exe := `/home/name & space/50%/wallapop`
	dir := `/home/name & space/state`
	systemd := authServiceDefinition("systemd", "test", exe, dir, nil)
	if !strings.Contains(systemd, `ExecStart="/home/name & space/50%%/wallapop" auth service run --dir "/home/name & space/state" --no-input`) || strings.Contains(systemd, "Environment=") {
		t.Fatal(systemd)
	}
	plist := authServiceDefinition("launchd", "test", exe, dir, nil)
	wellFormed(t, plist)
	if strings.Contains(plist, "WALLAPOP_SESSION_TOKEN") || strings.Contains(systemd, "api.token") || strings.Contains(plist, "EnvironmentVariables") {
		t.Fatal("secret in service definition")
	}
	if !strings.Contains(plist, "name &amp; space") {
		t.Fatal("paths not XML escaped")
	}
}

// The renewal service must keep using the proxy the CLI uses, or it would
// renew directly and the CLI would refuse the engine as mismatched.
func TestAuthServiceDefinitionsPinTheProxy(t *testing.T) {
	env := [][2]string{{"WALLAPOP_PROXY", "socks5h://127.0.0.1:1080/?a=%&b"}}
	systemd := authServiceDefinition("systemd", "test", "/bin/wallapop", "/state", env)
	if !strings.Contains(systemd, `Environment="WALLAPOP_PROXY=socks5h://127.0.0.1:1080/?a=%%&b"`+"\nExecStart=") {
		t.Fatal(systemd)
	}
	plist := authServiceDefinition("launchd", "test", "/bin/wallapop", "/state", env)
	wellFormed(t, plist)
	if !strings.Contains(plist, "<key>EnvironmentVariables</key><dict><key>WALLAPOP_PROXY</key><string>socks5h://127.0.0.1:1080/?a=%&amp;b</string></dict>") {
		t.Fatal(plist)
	}
}

// A proxy set in a config.toml found through WALLAPOP_CONFIG or a custom
// XDG_CONFIG_HOME only reaches the service if the service finds that file.
func TestAuthServiceDefinitionsFindTheSameConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/home/u/cfg")
	t.Setenv("WALLAPOP_CONFIG", "/home/u/other/config.toml")
	t.Setenv("WALLAPOP_PROXY", "")
	t.Setenv("WALLAPOP_SESSION_TOKEN", "secret-cookie")
	systemd := authServiceDefinition("systemd", "test", "/bin/wallapop", "/state", serviceEnv())
	for _, want := range []string{`Environment="XDG_CONFIG_HOME=/home/u/cfg"`, `Environment="WALLAPOP_CONFIG=/home/u/other/config.toml"`} {
		if !strings.Contains(systemd, want) {
			t.Fatalf("missing %s:\n%s", want, systemd)
		}
	}
	if strings.Contains(systemd, "secret-cookie") {
		t.Fatal("session secret in the unit")
	}
	plist := authServiceDefinition("launchd", "test", "/bin/wallapop", "/state", serviceEnv())
	wellFormed(t, plist)
	if !strings.Contains(plist, "<key>WALLAPOP_CONFIG</key><string>/home/u/other/config.toml</string>") {
		t.Fatal(plist)
	}
}

func wellFormed(t *testing.T, plist string) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewBufferString(plist))
	for {
		_, err := d.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			return
		}
	}
}
