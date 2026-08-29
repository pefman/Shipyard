package listen

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

// DefaultStateFile is where listen mode persists which issues have been
// processed when Options.StateFile is empty: a file in the current
// working directory, so a container can mount it (or any directory) to
// make the state survive restarts.
const DefaultStateFile = "shipyard-listen-state.json"

// State records which issues have been processed so a restart (or a
// later poll) does not solve them again. It is persisted as a JSON file
// mapping issue number to the resulting pull request URL ("" for dry
// runs, which open no PR).
type State struct {
	// Processed maps issue number (as a string, since JSON object keys
	// are strings) to the pull request URL it produced, or "" for dry
	// runs.
	Processed map[string]string `json:"processed"`
}

// stateFile resolves Options.StateFile against the default.
func stateFile(configured string) string {
	if configured != "" {
		return configured
	}
	return DefaultStateFile
}

// LoadState reads the state file at path. A missing file is not an
// error: it means nothing has been processed yet.
func LoadState(path string) (*State, error) {
	s := &State{Processed: make(map[string]string)}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("decoding state file %s: %w", path, err)
	}
	if s.Processed == nil {
		s.Processed = make(map[string]string)
	}
	return s, nil
}

// Remember records that issue number has been processed with the given
// pull request URL ("" for dry runs).
func (s *State) Remember(number int, prURL string) {
	if s.Processed == nil {
		s.Processed = make(map[string]string)
	}
	s.Processed[strconv.Itoa(number)] = prURL
}

// IsProcessed reports whether issue number is recorded in the state.
func (s *State) IsProcessed(number int) (prURL string, ok bool) {
	prURL, ok = s.Processed[strconv.Itoa(number)]
	return
}

// Save writes the state to path atomically (temp file + rename), so a
// crash mid-save cannot leave a truncated file behind.
func (s *State) Save(path string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding state: %w", err)
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("creating directory for state file %s: %w", path, err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".listen-state-*.tmp")
	if err != nil {
		return fmt.Errorf("writing state file %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("writing state file %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("writing state file %s: %w", path, err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("moving state file into place at %s: %w", path, err)
	}
	return nil
}
