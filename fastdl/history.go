package fastdl

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// HistoryEntry is one finished download shown in the HISTORY panel.
type HistoryEntry struct {
	URL string    `json:"url"`
	At  time.Time `json:"at"`
}

// History persists download history across restarts (HISTORY panel +
// RESET CLEAR in the UI).
type History struct {
	mu      sync.RWMutex
	path    string
	entries []HistoryEntry
}

// LoadHistory reads (or initializes) the history file inside dir.
func LoadHistory(dir string) *History {
	h := &History{
		path: filepath.Join(dir, "ytblaze-history.json"),
	}
	data, err := os.ReadFile(h.path)
	if err == nil {
		_ = json.Unmarshal(data, &h.entries)
	}
	return h
}

// Add appends an entry and saves immediately.
func (h *History) Add(url string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = append(h.entries, HistoryEntry{URL: url, At: time.Now()})
	// keep the file bounded
	if len(h.entries) > 200 {
		h.entries = h.entries[len(h.entries)-200:]
	}
	h.saveLocked()
}

// List returns entries newest first.
func (h *History) List() []HistoryEntry {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]HistoryEntry, 0, len(h.entries))
	for i := len(h.entries) - 1; i >= 0; i-- {
		out = append(out, h.entries[i])
	}
	return out
}

// Clear wipes history (RESET CLEAR button) and deletes the file content.
func (h *History) Clear() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.entries = nil
	h.saveLocked()
}

func (h *History) saveLocked() {
	if len(h.entries) == 0 {
		_ = os.Remove(h.path)
		return
	}
	data, err := json.MarshalIndent(h.entries, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(h.path, data, 0644)
}
