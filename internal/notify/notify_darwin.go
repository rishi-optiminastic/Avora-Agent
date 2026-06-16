//go:build darwin

// Package notify surfaces a ping to the user — a sound plus an on-screen
// message. macOS uses the built-in "Ping" system sound and an AppleScript
// dialog so the message is hard to miss.
package notify

import (
	"fmt"
	"os/exec"
	"strings"
)

const soundPath = "/System/Library/Sounds/Ping.aiff"

// Ping plays a sound and shows the message in a dialog (non-blocking).
func Ping(message string) {
	go func() { _ = exec.Command("afplay", soundPath).Run() }()

	text := strings.TrimSpace(message)
	if text == "" {
		text = "Your manager pinged you on Avora."
	}
	script := fmt.Sprintf(
		`display dialog %s with title "Avora" buttons {"OK"} default button "OK" giving up after 120`,
		appleScriptString(text),
	)
	go func() { _ = exec.Command("osascript", "-e", script).Run() }()
}

// appleScriptString quotes a Go string as an AppleScript string literal.
func appleScriptString(s string) string {
	return `"` + strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(s) + `"`
}
