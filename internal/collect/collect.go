// Package collect captures a single observation of user activity — the
// frontmost application and how long the user has been idle. The actual
// gathering is platform-specific (see collect_darwin.go).
package collect

// Sample is one observation. ActiveWindow is the foreground app name (may be
// empty if it can't be read); IdleSeconds is seconds since the last input; URL
// is the active browser tab's URL when the foreground app is a browser (else "").
type Sample struct {
	ActiveWindow string
	IdleSeconds  int
	URL          string
	// PageTitle is the active browser tab's title; Browser is the browser app
	// name (both "" for non-browsers or when Automation permission is denied).
	PageTitle string
	Browser   string
}
