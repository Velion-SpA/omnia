package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Store persists sync cursors as a JSON file.
type Store struct {
	mu      sync.Mutex
	path    string
	cursors map[string]string // key: "source:key"
}

// DefaultPath returns the default state file path.
func DefaultPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state", "omnia", "state.json")
}

// New loads (or creates) the state file at the given path.
func New(path string) (*Store, error) {
	s := &Store{
		path:    path,
		cursors: make(map[string]string),
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("read state file: %w", err)
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.cursors); err != nil {
			return nil, fmt.Errorf("parse state file: %w", err)
		}
	}
	return s, nil
}

func (s *Store) GetCursor(source, key string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.cursors[source+":"+key]
	return v, ok
}

func (s *Store) SetCursor(source, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[source+":"+key] = value
	return nil
}

func (s *Store) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(s.cursors, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(s.path, data, 0o644); err != nil {
		return fmt.Errorf("write state file: %w", err)
	}
	return nil
}
