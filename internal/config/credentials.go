package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/Microck/wallapop-cli/internal/filelock"
)

func CloneCredentials(in Credentials) Credentials {
	out := Credentials{Profiles: map[string]Session{}}
	for k, v := range in.Profiles {
		out.Profiles[k] = v
	}
	return out
}

// sameSession compares persisted timestamps by instant, not by time-zone
// pointers or monotonic clock data that may differ after a TOML round trip.
func sameSession(a, b Session) bool {
	return a.GalletonID == b.GalletonID && a.SessionCookie == b.SessionCookie &&
		a.DeviceID == b.DeviceID && a.UserHash == b.UserHash && a.Name == b.Name &&
		a.SessionExpires.Equal(b.SessionExpires) && a.UpdatedAt.Equal(b.UpdatedAt)
}

// MergeCredentials preserves unrelated profiles saved by concurrent commands.
// A stale command cannot resurrect a deleted session or overwrite a reconnect.
func MergeCredentials(path string, before, after Credentials) (Credentials, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return Credentials{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lock, err := filelock.Acquire(ctx, path+".lock")
	if err != nil {
		return Credentials{}, err
	}
	defer filelock.Release(lock)
	disk, err := LoadCredentials(path)
	if err != nil {
		return Credentials{}, err
	}
	keys := map[string]bool{}
	for k := range before.Profiles {
		keys[k] = true
	}
	for k := range after.Profiles {
		keys[k] = true
	}
	for k := range keys {
		old, had := before.Profiles[k]
		next, has := after.Profiles[k]
		if had == has && sameSession(old, next) {
			continue
		}
		current, exists := disk.Profiles[k]
		if exists != had || !sameSession(current, old) {
			// Concurrent first-use migrations to the same daemon ID are equivalent.
			left, right := current, next
			left.UpdatedAt = time.Time{}
			right.UpdatedAt = time.Time{}
			if has && exists && next.GalletonID != "" && sameSession(left, right) {
				continue
			}
			if !has && !exists {
				continue
			}
			return Credentials{}, errors.New("profile metadata changed concurrently; retry the command (the daemon session was not overwritten)")
		}
		if has {
			disk.Profiles[k] = next
		} else {
			delete(disk.Profiles, k)
		}
	}
	if err := SaveCredentials(path, disk); err != nil {
		return Credentials{}, err
	}
	return disk, nil
}
