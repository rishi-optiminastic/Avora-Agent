//go:build darwin

package collect

import (
	"os/exec"
	"strconv"
	"strings"
)

// Collect reads the frontmost app (via System Events), the HID idle time (via
// ioreg), and — when the foreground app is a browser — the active tab URL, all
// on macOS. Reading the app name needs Accessibility permission; reading a
// browser URL needs Automation permission for that browser. If denied, the
// corresponding field is "".
func Collect() (Sample, error) {
	app := frontmostApp()
	url, title, browser := browserTab(app)
	return Sample{
		ActiveWindow: app,
		IdleSeconds:  idleSeconds(),
		URL:          url,
		PageTitle:    title,
		Browser:      browser,
	}, nil
}

// browserTab asks the named browser for its active tab URL + title via
// AppleScript. Returns ("", "", "") for non-browsers or when permission is
// denied; otherwise also returns the browser app name.
func browserTab(app string) (url, title, browser string) {
	var urlScript, titleScript string
	switch app {
	case "Google Chrome", "Google Chrome Canary", "Chromium", "Brave Browser",
		"Microsoft Edge", "Arc", "Vivaldi", "Opera":
		urlScript = `tell application "` + app + `" to get URL of active tab of front window`
		titleScript = `tell application "` + app + `" to get title of active tab of front window`
	case "Safari", "Safari Technology Preview":
		urlScript = `tell application "` + app + `" to get URL of front document`
		titleScript = `tell application "` + app + `" to get name of front document`
	default:
		return "", "", ""
	}
	return runScript(urlScript), runScript(titleScript), app
}

func runScript(script string) string {
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func frontmostApp() string {
	const script = `tell application "System Events" to get name of first application process whose frontmost is true`
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// idleSeconds parses HIDIdleTime (nanoseconds) from ioreg.
func idleSeconds() int {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem").Output()
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.Contains(line, "HIDIdleTime") {
			continue
		}
		idx := strings.LastIndex(line, "=")
		if idx < 0 {
			return 0
		}
		ns, err := strconv.ParseInt(strings.TrimSpace(line[idx+1:]), 10, 64)
		if err != nil {
			return 0
		}
		return int(ns / 1_000_000_000)
	}
	return 0
}
