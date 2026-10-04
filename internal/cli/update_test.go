package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestUpdateConsent(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  bool
	}{{"\n", true}, {"y\n", true}, {"YES\n", true}, {"n\n", false}, {"garbage\n", false}, {"", false}} {
		var out bytes.Buffer
		a := App{Version: "0.1.0", Stdin: strings.NewReader(tc.input), Stderr: &out}
		if got := a.updateConsent("v0.2.0"); got != tc.want {
			t.Errorf("%q: %v", tc.input, got)
		}
		if !strings.Contains(out.String(), "[Y/n]") {
			t.Fatal(out.String())
		}
	}
}

func TestAutomaticUpdateSkipsNonInteractiveAndDevelopment(t *testing.T) {
	for _, a := range []*App{{Version: "0.1.0"}, {Version: "dev", Interactive: true}} {
		a.maybeUpdate(context.Background(), a.rootCmd())
	}
}

func TestUpdateDevelopmentBuild(t *testing.T) {
	var out, stderr bytes.Buffer
	code := Execute("dev", []string{"update", "--yes"}, strings.NewReader(""), &out, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "development") {
		t.Fatalf("%d: %s", code, stderr.String())
	}
}
