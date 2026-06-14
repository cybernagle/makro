package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// DeviceStore persists APNs device tokens keyed by device_id, so multiple
// devices (e.g. iPhone + iPad) can each receive pushes. Tokens are upserted on
// every app launch (APNs tokens change on reinstall / OS restore).
type DeviceStore struct {
	mu     sync.Mutex
	path   string
	tokens map[string]string // device_id → hex push token
}

func NewDeviceStore(path string) *DeviceStore {
	ds := &DeviceStore{path: path, tokens: make(map[string]string)}
	ds.load()
	return ds
}

func (s *DeviceStore) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return // missing file is fine — start empty
	}
	if err := json.Unmarshal(data, &s.tokens); err != nil {
		return
	}
}

// Upsert records a device token and persists immediately.
func (s *DeviceStore) Upsert(deviceID, token string) {
	if deviceID == "" || token == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[deviceID] = token
	if err := s.saveLocked(); err != nil {
		// Keep the in-memory value; next successful write persists it.
		os.Stderr.WriteString("device_store save: " + err.Error() + "\n")
	}
}

// All returns a snapshot of all known push tokens.
func (s *DeviceStore) All() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.tokens))
	for _, t := range s.tokens {
		out = append(out, t)
	}
	return out
}

func (s *DeviceStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
