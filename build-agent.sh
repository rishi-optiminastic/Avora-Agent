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
# at that repo's latest release. AVORA_FE_URL defaults to the Vercel app.
set -euo pipefail
cd "$(dirname "$0")"

FE_URL="${AVORA_FE_URL:-https://avora-fe.vercel.app}"
API_URL="${AVORA_API_URL:?set AVORA_API_URL to your backend host, e.g. https://avora-be.onrender.com}"

LDFLAGS="-s -w \
  -X avora-agent/internal/config.defaultFEURL=${FE_URL} \
  -X avora-agent/internal/config.defaultAPIURL=${API_URL}"

OUT="dist"
rm -rf "$OUT" && mkdir -p "$OUT"

echo "Building with FE=${FE_URL}  API=${API_URL}"
GOOS=darwin  GOARCH=arm64 go build -ldflags "$LDFLAGS" -o "$OUT/avora-agent-macos-arm64" ./cmd/avora-agent
GOOS=darwin  GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$OUT/avora-agent-macos-intel" ./cmd/avora-agent
GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o "$OUT/avora-agent.exe" ./cmd/avora-agent

echo "Done → $OUT"
ls -lh "$OUT"
echo
echo "Next: attach these to a GitHub Release, e.g."
echo "  gh release create v0.1.0 ./dist/* --repo <owner>/<repo> --title v0.1.0 --notes 'Agent build'"
