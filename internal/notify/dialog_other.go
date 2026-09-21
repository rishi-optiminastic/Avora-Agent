//go:build !windows

package notify

// Dialog is a no-op off Windows: every other platform the agent installs on
// runs the command from a terminal, so the message is already on screen.
func Dialog(_, _ string, _ bool) {}
