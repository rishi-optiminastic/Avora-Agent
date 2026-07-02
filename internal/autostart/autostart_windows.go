//go:build windows

package autostart

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

const (
	taskName  = `AvoraAgent`
	legacyKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`
	legacyVal = "AvoraAgent"
)

// Enable registers a per-user Scheduled Task that launches the agent at logon
// and — unlike a plain Run-key entry, which fires once and is never watched —
// keeps it alive: the task repeats every few minutes and Task Scheduler
// relaunches the agent if it has crashed or been killed, while refusing to start
// a second copy when one is already running. This is the Windows equivalent of
// launchd's KeepAlive on macOS. No admin rights are needed (it runs as the
// logged-in user).
func Enable(execPath string) error {
	// Drop any legacy Run-key autostart from older installs so the agent isn't
	// launched twice (once by the key, once by the task).
	_ = exec.Command("reg", "delete", legacyKey, "/v", legacyVal, "/f").Run()

	xmlPath := filepath.Join(os.TempDir(), "avora-agent-task.xml")
	// schtasks /xml expects UTF-16 (with BOM); plain UTF-8 is rejected on some
	// builds with an opaque "task XML is malformed" error.
	if err := os.WriteFile(xmlPath, utf16LE(taskXML(execPath)), 0o644); err != nil {
		return err
	}
	defer func() { _ = os.Remove(xmlPath) }()

	// /f overwrites an existing definition, so re-running install upgrades it.
	if err := exec.Command(
		"schtasks", "/create", "/tn", taskName, "/xml", xmlPath, "/f",
	).Run(); err != nil {
		return err
	}

	// Kick it off now rather than waiting for the next logon — install typically
	// runs mid-session, and a LogonTrigger only fires on a *future* logon.
	// Best-effort: the task's own RegistrationTrigger (below) fires on its own
	// moments after /create succeeds, so this is a fast path, not the only path.
	// Going through schtasks instead of spawning the process ourselves matters:
	// Task Scheduler then tracks the instance, so IgnoreNew correctly stops the
	// other triggers from spawning a duplicate, and the process isn't a child of
	// this installer's console/job — it survives the terminal being closed right
	// after "install" finishes, which a raw detached child process would not.
	_ = exec.Command("schtasks", "/run", "/tn", taskName).Run()
	return nil
}

// Disable removes the Scheduled Task (and any legacy Run-key entry).
func Disable() error {
	_ = exec.Command("reg", "delete", legacyKey, "/v", legacyVal, "/f").Run()
	return exec.Command("schtasks", "/delete", "/tn", taskName, "/f").Run()
}

// taskXML builds the Task Scheduler definition. Key settings:
//   - RegistrationTrigger with an indefinite 3-minute Repetition: fires moments
//     after /create succeeds and keeps re-checking regardless of logon state, so
//     a mid-session install (the common case) or a later crash self-heals within
//     ~3 min without waiting for the user to log out and back in.
//   - LogonTrigger with the same Repetition: covers future fresh logons too.
//   - MultipleInstancesPolicy=IgnoreNew: the repeats never start a second copy.
//   - RestartOnFailure: covers a fast crash on startup.
//   - ExecutionTimeLimit=PT0S and StopIfGoingOnBatteries=false: never auto-killed.
func taskXML(execPath string) string {
	return `<?xml version="1.0" encoding="UTF-16"?>
<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo>
    <Description>Avora activity agent — keeps running and restarts automatically if it stops.</Description>
  </RegistrationInfo>
  <Triggers>
    <RegistrationTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT3M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </RegistrationTrigger>
    <LogonTrigger>
      <Enabled>true</Enabled>
      <Repetition>
        <Interval>PT3M</Interval>
        <StopAtDurationEnd>false</StopAtDurationEnd>
      </Repetition>
    </LogonTrigger>
  </Triggers>
  <Principals>
    <Principal id="Author">
      <LogonType>InteractiveToken</LogonType>
      <RunLevel>LeastPrivilege</RunLevel>
    </Principal>
  </Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>false</AllowHardTerminate>
    <StartWhenAvailable>true</StartWhenAvailable>
    <RunOnlyIfNetworkAvailable>false</RunOnlyIfNetworkAvailable>
    <IdleSettings>
      <StopOnIdleEnd>false</StopOnIdleEnd>
      <RestartOnIdle>false</RestartOnIdle>
    </IdleSettings>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>false</Hidden>
    <RunOnlyIfIdle>false</RunOnlyIfIdle>
    <WakeToRun>false</WakeToRun>
    <ExecutionTimeLimit>PT0S</ExecutionTimeLimit>
    <Priority>7</Priority>
    <RestartOnFailure>
      <Interval>PT1M</Interval>
      <Count>3</Count>
    </RestartOnFailure>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(execPath) + `</Command>
      <Arguments>run</Arguments>
    </Exec>
  </Actions>
</Task>`
}

// xmlEscape escapes the few characters that can appear in a Windows path (e.g. an
// "&" in a username) and would otherwise break the task XML.
func xmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return r.Replace(s)
}

// utf16LE encodes s as little-endian UTF-16 with a BOM, the form schtasks wants.
func utf16LE(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2+len(u)*2)
	b = append(b, 0xFF, 0xFE) // BOM
	for _, r := range u {
		b = append(b, byte(r), byte(r>>8))
	}
	return b
}
