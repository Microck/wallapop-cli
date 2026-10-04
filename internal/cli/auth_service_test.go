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
	systemd := authServiceDefinition("systemd", "test", exe, dir)
	if !strings.Contains(systemd, `ExecStart="/home/name & space/50%%/wallapop" auth service run --dir "/home/name & space/state" --no-input`) {
		t.Fatal(systemd)
	}
	plist := authServiceDefinition("launchd", "test", exe, dir)
	d := xml.NewDecoder(bytes.NewBufferString(plist))
	for {
		_, err := d.Token()
		if err != nil {
			if err.Error() != "EOF" {
				t.Fatal(err)
			}
			break
		}
	}
	if strings.Contains(plist, "WALLAPOP_SESSION_TOKEN") || strings.Contains(systemd, "api.token") {
		t.Fatal("secret in service definition")
	}
	if !strings.Contains(plist, "name &amp; space") {
		t.Fatal("paths not XML escaped")
	}
}
