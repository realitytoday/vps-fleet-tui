// Package theme holds the user-configurable colours shared by the UI and plugins.
package theme

import "sync"

var (
	mu    sync.RWMutex
	start = "#5A56E0" // same as progress.WithDefaultGradient
	end   = "#EE6FF8"
)

// Gradient returns the start and end colour of usage bars and trend lines.
func Gradient() (string, string) {
	mu.RLock()
	defer mu.RUnlock()
	return start, end
}

// SetGradient changes the bar colours (hex, e.g. "#5A56E0").
func SetGradient(s, e string) {
	mu.Lock()
	defer mu.Unlock()
	start, end = s, e
}
