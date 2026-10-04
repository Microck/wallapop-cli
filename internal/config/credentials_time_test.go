package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMergeCredentialsWithFractionalHourOffsets(t *testing.T) {
	for _, offset := range []int{5*3600 + 30*60, 5*3600 + 45*60, -3*3600 - 30*60} {
		instant := time.Date(2026, 10, 4, 12, 0, 0, 123, time.FixedZone("", offset))
		for _, operation := range []string{"migrate", "logout"} {
			t.Run(instant.Format("-07:00")+"/"+operation, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "credentials.toml")
				original := Credentials{Profiles: map[string]Session{"one": {
					SessionCookie: "legacy-cookie", DeviceID: "device", UserHash: "user", Name: "account",
					SessionExpires: instant.Add(time.Hour), UpdatedAt: instant,
				}}}
				if err := SaveCredentials(path, original); err != nil {
					t.Fatal(err)
				}
				before, err := LoadCredentials(path)
				if err != nil {
					t.Fatal(err)
				}
				after := CloneCredentials(before)
				if operation == "logout" {
					delete(after.Profiles, "one")
				} else {
					next := after.Profiles["one"]
					next.SessionCookie, next.GalletonID = "", "managed-one"
					next.SessionExpires = time.Time{}
					next.UpdatedAt = instant.Add(time.Minute).UTC()
					after.Profiles["one"] = next
				}
				if _, err := MergeCredentials(path, before, after); err != nil {
					t.Fatal(err)
				}
				saved, err := LoadCredentials(path)
				if err != nil {
					t.Fatal(err)
				}
				s, exists := saved.Profiles["one"]
				if operation == "logout" {
					if exists {
						t.Fatal("logout left a session")
					}
				} else if !exists || s.GalletonID != "managed-one" || s.SessionCookie != "" || s.DeviceID != "device" || s.UserHash != "user" || s.Name != "account" || !s.SessionExpires.IsZero() || !s.UpdatedAt.Equal(after.Profiles["one"].UpdatedAt) {
					t.Fatal("migration did not preserve the expected metadata")
				}
			})
		}
	}
}

func TestMergeCredentialsIgnoresTimeRepresentationOnlyChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	instant := time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("", 5*3600+30*60))
	old := Session{SessionCookie: "old", SessionExpires: instant.Add(time.Hour), UpdatedAt: instant}
	before := Credentials{Profiles: map[string]Session{"one": old}}
	after := CloneCredentials(before)
	equivalent := old
	equivalent.SessionExpires = equivalent.SessionExpires.UTC()
	equivalent.UpdatedAt = equivalent.UpdatedAt.UTC()
	after.Profiles["one"] = equivalent
	// Another process has reconnected. A representation-only local change must
	// remain a no-op, not conflict with or overwrite the newer disk state.
	current := old
	current.SessionCookie = "reconnected"
	if err := SaveCredentials(path, Credentials{Profiles: map[string]Session{"one": current}}); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeCredentials(path, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Profiles["one"].SessionCookie != "reconnected" {
		t.Fatal("overwrote a concurrent reconnect")
	}
}

func TestMergeCredentialsAcceptsEquivalentMigrationInstants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	instant := time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("", 5*3600+45*60))
	before := Credentials{Profiles: map[string]Session{"one": {SessionCookie: "old"}}}
	next := Session{GalletonID: "managed-one", SessionExpires: instant, UpdatedAt: instant}
	after := Credentials{Profiles: map[string]Session{"one": next}}
	current := next
	current.UpdatedAt = instant.Add(time.Second)
	if err := SaveCredentials(path, Credentials{Profiles: map[string]Session{"one": current}}); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeCredentials(path, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if !merged.Profiles["one"].UpdatedAt.Equal(current.UpdatedAt) {
		t.Fatal("overwrote the winning migration")
	}
}

func TestMergeCredentialsRejectsChangedInstants(t *testing.T) {
	for _, field := range []string{"session_expires", "updated_at"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "credentials.toml")
			instant := time.Date(2026, 10, 4, 12, 0, 0, 0, time.FixedZone("", -3*3600-30*60))
			old := Session{SessionCookie: "old", SessionExpires: instant.Add(time.Hour), UpdatedAt: instant}
			before := Credentials{Profiles: map[string]Session{"one": old}}
			current := old
			if field == "session_expires" {
				current.SessionExpires = current.SessionExpires.Add(time.Second)
			} else {
				current.UpdatedAt = current.UpdatedAt.Add(time.Second)
			}
			if err := SaveCredentials(path, Credentials{Profiles: map[string]Session{"one": current}}); err != nil {
				t.Fatal(err)
			}
			disk, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := MergeCredentials(path, before, Credentials{Profiles: map[string]Session{"one": {GalletonID: "managed-one"}}}); err == nil {
				t.Fatal("ignored a real timestamp change")
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(saved) != string(disk) {
				t.Fatal("conflict changed saved credentials")
			}
		})
	}
}
