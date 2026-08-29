package listen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadStateMissingFileIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState on a missing file: %v", err)
	}
	if len(st.Processed) != 0 {
		t.Errorf("state = %+v, want empty", st)
	}
	if _, ok := st.IsProcessed(1); ok {
		t.Error("nothing should be processed in a fresh state")
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	st := &State{}
	st.Remember(11, "https://github.com/towner/trepo/pull/50")
	st.Remember(12, "") // dry run: no pull request
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if url, ok := loaded.IsProcessed(11); !ok || url != "https://github.com/towner/trepo/pull/50" {
		t.Errorf("issue 11 = %q (processed=%v), want the PR URL", url, ok)
	}
	if url, ok := loaded.IsProcessed(12); !ok || url != "" {
		t.Errorf("issue 12 = %q (processed=%v), want processed with no PR URL", url, ok)
	}
	if _, ok := loaded.IsProcessed(13); ok {
		t.Error("issue 13 was never recorded")
	}
}

func TestSaveCreatesParentDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "dirs", "state.json")
	st := &State{}
	st.Remember(7, "url")
	if err := st.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("state file not on disk: %v", err)
	}
}

func TestLoadStateRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadState(path); err == nil {
		t.Fatal("LoadState: expected an error for a corrupt file")
	}
}
