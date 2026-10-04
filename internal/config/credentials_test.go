package config

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestConcurrentProfileMigrationsPreserveEachOther(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	before := Credentials{Profiles: map[string]Session{"one": {SessionCookie: "one"}, "two": {SessionCookie: "two"}}}
	if err := SaveCredentials(path, before); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, name := range []string{"one", "two"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			next := CloneCredentials(before)
			next.Profiles[name] = Session{GalletonID: name}
			if _, err := MergeCredentials(path, before, next); err != nil {
				t.Error(err)
			}
		}(name)
	}
	wg.Wait()
	after, err := LoadCredentials(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		if after.Profiles[name].GalletonID != name || after.Profiles[name].SessionCookie != "" {
			t.Fatal("concurrent migration lost:", after)
		}
	}
}
func TestStaleMetadataCannotResurrectLogout(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.toml")
	before := Credentials{Profiles: map[string]Session{"one": {SessionCookie: "old"}}}
	if err := SaveCredentials(path, before); err != nil {
		t.Fatal(err)
	}
	if _, err := MergeCredentials(path, before, Credentials{}); err != nil {
		t.Fatal(err)
	}
	next := Credentials{Profiles: map[string]Session{"one": {GalletonID: "one"}}}
	if _, err := MergeCredentials(path, before, next); err == nil {
		t.Fatal("stale migration resurrected logout")
	}
}
