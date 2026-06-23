#!/usr/bin/env bash
# Build release agent binaries with production URLs baked in, into ./dist, then
# attach them to a GitHub Release (the download page links to the repo's latest
# release).
#
#   AVORA_API_URL=https://avora-be.onrender.com ./build-agent.sh
#   # then, with the GitHub CLI:
#   gh release create v0.1.0 ./dist/* --repo <owner>/<repo> --title v0.1.0 --notes "Agent build"
#   # (or upload to an existing release):
#   gh release upload v0.1.0 ./dist/* --repo <owner>/<repo> --clobber
#
# Set NEXT_PUBLIC_AGENT_REPO=<owner>/<repo> on Vercel so the download page points
# at that repo's latest release. AVORA_FE_URL defaults to the canonical app domain
# — it's the host the agent opens for device enrollment ({FE}/agent/enroll).
set -euo pipefail
cd "$(dirname "$0")"

FE_URL="${AVORA_FE_URL:-https://avora.optiminastic.com}"
API_URL="${AVORA_API_URL:?set AVORA_API_URL to your backend host, e.g. https://avora-be.onrender.com}"

LDFLAGS="-s -w \
  -X avora-agent/internal/config.defaultFEURL=${FE_URL} \
  -X avora-agent/internal/config.defaultAPIURL=${API_URL}"

OUT="dist"
rm -rf "$OUT" && mkdir -p "$OUT"

echo "Building with FE=${FE_URL}  API=${API_URL}"
GOOS=darwin  GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$OUT/avora-agent-macos-arm64" ./cmd/avora-agent
GOOS=darwin  GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$OUT/avora-agent-macos-intel" ./cmd/avora-agent
# -H windowsgui → GUI subsystem: no console window when Windows auto-starts the
# agent at login (the persistent black cmd window). Interactive commands still
# print via AttachConsole (see cmd/avora-agent/console_windows.go).
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS -H windowsgui" -o "$OUT/avora-agent.exe" ./cmd/avora-agent

# macOS Screen Recording permission (TCC) is keyed on the binary's code
# signature. An UNSIGNED binary changes identity every rebuild, so macOS
# re-prompts each time. Sign with a STABLE identity so the grant persists across
# rebuilds: set CODESIGN_IDENTITY to a "Developer ID Application: …" cert (for
# distribution, also notarize) or a self-signed cert you reuse. Defaults to
# ad-hoc ("-"), which is enough to run but still re-prompts on each rebuild.
# (Windows has no such permission — employees on Windows are unaffected.)
SIGN_ID="${CODESIGN_IDENTITY:--}"
if command -v codesign >/dev/null 2>&1; then
  echo "Signing macOS binaries with identity: ${SIGN_ID}"
  if [ "$SIGN_ID" = "-" ]; then
    sign() { codesign --force --sign - "$1" || true; }   # ad-hoc (runs, but re-prompts per rebuild)
  else
    sign() { codesign --force --timestamp --options runtime --sign "$SIGN_ID" "$1" || true; }
  fi
  sign "$OUT/avora-agent-macos-arm64"
  sign "$OUT/avora-agent-macos-intel"
fi

echo "Done → $OUT"
ls -lh "$OUT"
echo
echo "Next: attach these to a GitHub Release, e.g."
echo "  gh release create v0.1.0 ./dist/* --repo <owner>/<repo> --title v0.1.0 --notes 'Agent build'"
