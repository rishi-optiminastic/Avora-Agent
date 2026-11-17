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

## Release (self-update)

Installed agents poll `releases/latest/download/VERSION` on `AGENT_REPO` and only swap in a new binary whose SHA-256 matches `SHA256SUMS` from the same release.
A release without `SHA256SUMS` (or with a stale one) is refused by every agent.

1. Pick a new version tag (e.g. `v0.8.0`); it must differ from the running one.
2. `VERSION=v0.8.0 AGENT_REPO=<owner>/<repo> ./build-agent.sh`
3. Create the release and upload ALL of `dist/`: `avora-agent.exe`, `avora-agent-macos-arm64`, `avora-agent-macos-intel`, `VERSION`, `SHA256SUMS`.
4. Mark it as the latest release.

Never edit a binary after `build-agent.sh` (that changes its hash); rebuild instead.

## Offline queue and health

- Samples and screenshots that cannot be delivered are kept in `~/.avora/queue/` (max 3000 items, 200 MB; oldest dropped first) and sent oldest-first with backoff (15 s doubling to 10 min).
- Screenshots wait up to 12 h; activity samples only 15 min, so a longer outage stays a gap rather than replaying as a burst of stale activity at reconnect.
- Sequence numbers are drawn at send time and pinned for retries, so they stay strictly increasing in the order the server receives them.
- Permanent rejections (4xx other than 401/403/408/409/429) are logged and dropped.
- Every ~5 min, and on any status change, the agent posts `POST /api/v1/devices/health` (screen capture / window info / browser URL status, last error, queue size). Not queued; a 404 from an older backend is logged once.

## Use

```bash
# Point at your environment (defaults are localhost:3000 / :8000).
export AVORA_FE_URL=https://avora-fe.vercel.app
export AVORA_API_URL=https://<your-backend-host>

./bin/avora-agent install   # opens the browser to enroll, then runs now +
                             # at every login (recommended — this is what
                             # the dashboard's download page tells people to run)
./bin/avora-agent status    # show enrollment / config
./bin/avora-agent uninstall # remove auto-start
```

`install` is the one-step path and starts tracking immediately — don't tell
people to run `enroll` and `run` separately, since `run` alone doesn't set up
auto-start and `enroll` alone doesn't start tracking. Those two remain as
building blocks (`enroll` links the device without starting anything; `run`
starts the sampling loop in the foreground) for local development below.

```bash
./bin/avora-agent enroll    # opens the browser; click "Connect this device"
./bin/avora-agent run       # starts tracking; Ctrl-C to stop
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
`scrot`; Wayland differs) and Windows browser-URL capture. Running as a launch
agent / background service is built (`install`, see `internal/autostart`) on
macOS and Windows; Linux autostart uses an XDG `.desktop` entry.
