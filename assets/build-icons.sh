#!/usr/bin/env bash
# Regenerate agent icons from avora-icon.svg (macOS only — uses qlmanage / sips /
# iconutil). Produces avora.icns (for a future macOS .app) and avora.ico, then
# embeds the .ico into the Windows build as resource_windows_amd64.syso.
#
# Run after changing the icon:  ./assets/build-icons.sh
# Requires the `rsrc` tool:     go install github.com/akavel/rsrc@latest
set -euo pipefail
cd "$(dirname "$0")"            # agent/assets

SVG="avora-icon.svg"
MASTER="avora-1024.png"
TMP="$(mktemp -d)"

# 1. SVG -> 1024 PNG via QuickLook (no third-party rasteriser needed).
qlmanage -t -s 1024 -o "$TMP" "$SVG" >/dev/null
cp "$TMP/$SVG.png" "$MASTER"

# 2. macOS .icns
ICS="$TMP/avora.iconset"; mkdir -p "$ICS"
for pair in "16:16x16" "32:16x16@2x" "32:32x32" "64:32x32@2x" \
            "128:128x128" "256:128x128@2x" "256:256x256" "512:256x256@2x" "512:512x512"; do
  px="${pair%%:*}"; name="${pair##*:}"
  sips -z "$px" "$px" "$MASTER" --out "$ICS/icon_$name.png" >/dev/null
done
cp "$MASTER" "$ICS/icon_512x512@2x.png"
iconutil -c icns "$ICS" -o avora.icns

# 3. Windows .ico (PNG-embedded entries) via a tiny inline Go encoder.
for s in 16 32 48 64 128 256; do sips -z $s $s "$MASTER" --out "$TMP/$s.png" >/dev/null; done
cat > "$TMP/mkico.go" <<'GO'
package main
import ("bytes";"encoding/binary";"image";_ "image/png";"os")
func main(){
  out:=os.Args[1]; var es []struct{w,h int;b []byte}
  for _,p:=range os.Args[2:]{ b,_:=os.ReadFile(p); c,_,_:=image.DecodeConfig(bytes.NewReader(b)); es=append(es,struct{w,h int;b []byte}{c.Width,c.Height,b}) }
  var buf bytes.Buffer
  binary.Write(&buf,binary.LittleEndian,uint16(0)); binary.Write(&buf,binary.LittleEndian,uint16(1)); binary.Write(&buf,binary.LittleEndian,uint16(len(es)))
  off:=uint32(6+16*len(es))
  d:=func(n int)byte{ if n>=256 {return 0}; return byte(n) }
  for _,e:=range es{ buf.WriteByte(d(e.w)); buf.WriteByte(d(e.h)); buf.WriteByte(0); buf.WriteByte(0); binary.Write(&buf,binary.LittleEndian,uint16(1)); binary.Write(&buf,binary.LittleEndian,uint16(32)); binary.Write(&buf,binary.LittleEndian,uint32(len(e.b))); binary.Write(&buf,binary.LittleEndian,off); off+=uint32(len(e.b)) }
  for _,e:=range es{ buf.Write(e.b) }
  os.WriteFile(out,buf.Bytes(),0o644)
}
GO
( cd "$TMP" && go mod init mkico >/dev/null 2>&1 && go run . "$OLDPWD/avora.ico" 16.png 32.png 48.png 64.png 128.png 256.png )

# 4. Embed the .ico into the Windows build (only windows/amd64 links a .syso).
rsrc -ico avora.ico -arch amd64 -o ../cmd/avora-agent/resource_windows_amd64.syso

rm -rf "$TMP"
echo "Done: avora.icns, avora.ico, cmd/avora-agent/resource_windows_amd64.syso"
