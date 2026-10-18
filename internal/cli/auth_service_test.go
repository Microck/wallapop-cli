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
