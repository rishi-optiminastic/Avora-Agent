# Avora Agent

The Avora desktop activity agent (macOS, this first build). It links a laptop to
an Avora account, then samples the foreground app + idle time and posts
HMAC-signed activity to the backend — the real data source behind the
Attendance / Activity dashboards (it replaces `be/scripts/seed_activity.py`).

This is the third Avora component, alongside `fe/` (Next.js) and `be/` (FastAPI).

## How it works

```
enroll:  agent ⇢ opens browser ⇢ /agent/enroll (user signed in) ⇢ POST /devices/self-enroll
         ⇠ token redirected to http://127.0.0.1:<port>/callback ⇠ (token saved 0600)

run:     every 60s →  frontmost app (osascript) + idle (ioreg HIDIdleTime)
                   →  HMAC-SHA256(body, key=device-token) in X-Signature
                   →  POST /api/v1/activity/ingest  (Bearer device-token)
```

- **No third-party dependencies** — Go standard library only.
- **Token storage:** `~/.avora/agent.json` (`0600`), holding the device token, the
  monotonic `sequence`, and the base URLs.
- **Sequence** is strictly increasing per device; it's persisted after each
  accepted sample so a restart resumes cleanly.

## Build

```bash
cd agent
go build -o bin/avora-agent ./cmd/avora-agent

# Cross-compile:
GOOS=windows GOARCH=amd64 go build -o bin/avora-agent.exe ./cmd/avora-agent
GOOS=linux   GOARCH=amd64 go build -o bin/avora-agent-linux ./cmd/avora-agent
```

## Use

```bash
# Point at your environment (defaults are localhost:3000 / :8000).
export AVORA_FE_URL=https://avora-fe.vercel.app
export AVORA_API_URL=https://<your-backend-host>

./bin/avora-agent enroll    # opens the browser; click "Connect this device"
./bin/avora-agent run       # starts tracking; Ctrl-C to stop
./bin/avora-agent status    # show enrollment / config
```

For local development, leave the env vars unset and run `fe` (`pnpm dev`) and
`be` (`uv run uvicorn app.main:app --reload`) first.

Every ~5 minutes it also captures a downscaled **screenshot** and uploads it to
`POST /api/v1/screenshots` (HMAC over the image bytes).

## Platform support

The collector/capture/notify layers are build-tagged per OS:

| Signal | macOS | Windows | Linux |
|---|---|---|---|
| Active app | ✅ `osascript` | ✅ Win32 `GetForegroundWindow` | ❌ stub |
| Idle time | ✅ `ioreg` | ✅ `GetLastInputInfo` | ❌ stub |
| Screenshot | ✅ `screencapture` | ✅ PowerShell `CopyFromScreen` | ❌ stub |
| Ping (sound+message) | ✅ `afplay` + dialog | ✅ `MessageBeep` + MessageBox | ❌ no-op |
| Active browser URL | ✅ AppleScript | ❌ (app name only) | ❌ |
| Enroll (open browser) | ✅ `open` | ✅ `rundll32` | ✅ `xdg-open` |

The binary builds and runs on all three; on Linux the data collectors are stubs
(the agent enrolls + polls but reports nothing) until Linux collectors land.

## macOS permissions

For whatever process runs the agent (e.g. your terminal), grant under
System Settings → Privacy & Security:

- **Accessibility** — to read the foreground app name (via System Events).
- **Automation** (per browser) — to read the active browser tab URL.
- **Screen Recording** — to capture screenshots.

Each degrades gracefully: without a permission the corresponding field is empty /
the screenshot is skipped, but the agent keeps running. Idle time needs none.

## Windows notes

No extra permissions are required. Browser-URL capture is macOS-only, so on
Windows the browsing breakdown uses app names only (no per-site categorisation).
Screenshot capture uses built-in PowerShell + System.Drawing.

## Not yet built

Linux collectors (active app / idle / screenshot — X11 via `xprop`/`xprintidle`/
`scrot`; Wayland differs), Windows browser-URL capture, and running as a launch
agent / background service.
