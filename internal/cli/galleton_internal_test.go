package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Microck/wallapop-cli/internal/galleton"
	"github.com/Microck/wallapop-cli/internal/wallapop"
)

func TestManagedSessionRetriesLocalPersistenceWithoutReimport(t *testing.T) {
	imports, headers, saves := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet:
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut:
			imports++
			_ = json.NewEncoder(w).Encode(galleton.Metadata{ID: "test", Provider: "wallapop"})
		default:
			headers++
			_ = json.NewEncoder(w).Encode(galleton.Headers{Headers: map[string]string{"Authorization": "Bearer access-token"}})
		}
	}))
	defer srv.Close()
	client, err := galleton.New(srv.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	s := &managedSession{
		id: "test", client: client, cookie: "legacy-cookie", origin: "https://es.wallapop.com", target: "https://api.wallapop.com",
		onReady: func() error {
			saves++
			if saves == 1 {
				return errors.New("disk full")
			}
			return nil
		},
	}
	if token, err := s.AccessToken(context.Background()); err == nil || token != "" || !strings.Contains(err.Error(), "migration could not be saved") {
		t.Fatalf("token released before metadata was saved: %q, %v", token, err)
	}
	for i := 0; i < 2; i++ {
		if token, err := s.AccessToken(context.Background()); err != nil || token != "access-token" {
			t.Fatalf("retry failed: %q, %v", token, err)
		}
	}
	if imports != 1 || saves != 2 || headers != 3 || s.cookie != "" {
		t.Fatalf("imports=%d saves=%d headers=%d; import-only cookie must be discarded", imports, saves, headers)
	}
}

func TestManagedIDsAndErrorClassification(t *testing.T) {
	a := &App{}
	a.Paths.CredentialsFile = filepath.Join(t.TempDir(), "credentials.toml")
	id := a.managedProfileID("name / Ω")
	if id != a.managedProfileID("name / Ω") || id == a.managedProfileID("other") || len(id) > 64 {
		t.Fatal("unstable or colliding profile IDs")
	}
	a.Paths.CredentialsFile = filepath.Join(t.TempDir(), "credentials.toml")
	if id == a.managedProfileID("name / Ω") {
		t.Fatal("distinct CLI homes collided")
	}
	if !errors.Is(managedError(context.Canceled), context.Canceled) {
		t.Fatal("lost cancellation")
	}
	for _, code := range []string{"renewal_uncertain", "reauth_required", "revision_conflict"} {
		err := managedError(&galleton.Error{Status: 409, Code: code})
		var apiErr *wallapop.Error
		if !errors.As(err, &apiErr) || apiErr.Kind != wallapop.KindAuth || apiErr.Retryable {
			t.Fatalf("unsafe replay classification: %v", err)
		}
	}
}
