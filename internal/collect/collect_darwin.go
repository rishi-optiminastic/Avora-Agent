//go:build darwin

package collect

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// scriptTimeout bounds every helper process this file starts.
//
// Nothing here used to have one, and all of it runs on the agent's main loop.
// An AppleScript `tell application` sends an Apple Event and waits for that app
// to answer, so a beachballing or mid-crash browser — or, on a fresh machine,
// an Automation permission prompt nobody clicks — blocked Output() with no
// bound at all. The whole agent froze: no samples, no screenshots, no pings,
// while the process sat there looking healthy. Neither launchd nor Task
// Scheduler restarts a process that has not exited, so the machine stayed off
// the fleet until someone rebooted it.
const scriptTimeout = 5 * time.Second

// Collect reads the frontmost app (via System Events), the HID idle time (via
// ioreg), and — when the foreground app is a browser — the active tab URL, all
// on macOS. Reading the app name needs Accessibility permission; reading a
// browser URL needs Automation permission for that browser. If denied, timed
// out, or slow, the corresponding field is "" and the loop carries on.
func Collect() (Sample, error) {
	app, appDenied := frontmostApp()
	url, title, browser, tabDenied := browserTab(app)
	return Sample{
		ActiveWindow:  app,
		IdleSeconds:   idleSeconds(),
		URL:           url,
		PageTitle:     title,
		Browser:       browser,
		WindowDenied:  appDenied,
		BrowserDenied: tabDenied,
	}, nil
}

// browserTab asks the named browser for its active tab URL + title via
// AppleScript. Returns ("", "", "") for non-browsers; otherwise also returns the
// browser app name, and denied=true when Automation permission was refused.
//
// URL and title come back from ONE script. They used to be two separate
// osascript runs, which meant two processes and two Apple Events per sample —
// double the cost and double the chance of catching the browser mid-hang.
func browserTab(app string) (url, title, browser string, denied bool) {
	var urlExpr, titleExpr string
	switch app {
	case "Google Chrome", "Google Chrome Canary", "Chromium", "Brave Browser",
		"Microsoft Edge", "Arc", "Vivaldi", "Opera":
		urlExpr, titleExpr = "URL of active tab of front window", "title of active tab of front window"
	case "Safari", "Safari Technology Preview":
		urlExpr, titleExpr = "URL of front document", "name of front document"
	default:
		return "", "", "", false
	}
	// `app` is always one of the literals above, never raw input, so it is safe
	// to interpolate. Newline-separated rather than a list, because a page title
	// can contain a comma but not a line break.
	script := `tell application "` + app + `"
		set u to ` + urlExpr + `
		set t to ` + titleExpr + `
	end tell
	return u & linefeed & t`
	out, err := runDetailed("osascript", "-e", script)
	if out == "" {
		return "", "", app, permissionDenied(err)
	}
	u, t, _ := strings.Cut(out, "\n")
	return strings.TrimSpace(u), strings.TrimSpace(t), app, false
}

// run executes a short helper and returns its trimmed stdout, or "" on any
// failure or timeout. CommandContext kills the child when the deadline passes;
// WaitDelay then stops Output() waiting on a pipe the dead child's descendants
// might still hold, which is the other way this could block forever.
func run(name string, args ...string) string {
	out, _ := runDetailed(name, args...)
	return out
}

// runDetailed is run plus the failure, so callers can tell a refused
// permission from an empty answer. Output() captures stderr into the
// *exec.ExitError, which is where osascript reports the denial.
func runDetailed(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), scriptTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// permissionMarkers are the osascript errors macOS returns when a TCC
// permission is missing: -1743 (Automation: "Not authorized to send Apple
// events"), -1719 / -25211 (Accessibility: "not allowed assistive access").
var permissionMarkers = []string{
	"-1743", "-1719", "-25211",
	"not authorized to send apple events",
	"not allowed assistive access",
}

// permissionDenied reports whether err is osascript refusing a permission.
func permissionDenied(err error) bool {
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	stderr := strings.ToLower(string(exitErr.Stderr))
	for _, marker := range permissionMarkers {
		if strings.Contains(stderr, marker) {
			return true
		}
	}
	return false
}

func frontmostApp() (app string, denied bool) {
	const script = `tell application "System Events" to get name of first application process whose frontmost is true`
	out, err := runDetailed("osascript", "-e", script)
	return out, permissionDenied(err)
}

// idleSeconds parses HIDIdleTime (nanoseconds) from ioreg.
func idleSeconds() int {
	out := run("ioreg", "-c", "IOHIDSystem")
	if out == "" {
		return 0
	}
	for _, line := range strings.Split(out, "\n") {
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
