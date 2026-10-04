#!/bin/sh
# Build the RG35XX (H700, linux/arm64) package of SDRg35xx.
# Output: dist/rg35xx/{SDRg35xx/,SDRg35xx.sh,SDRg35xx.png} — copy all into
# Roms/APPS.
set -e
cd "$(dirname "$0")"
mkdir -p dist/rg35xx/SDRg35xx

echo "== cross-compiling SDRg35xx (linux/arm64, pure Go) =="
# The stamp (YYYYMMDDHHMM) is both the OTA version number (compared by
# the app against version.txt on the download server) and the build id.
STAMP="$(date +%Y%m%d%H%M)"
# Underscore instead of a space: -X values must survive ldflags quoting.
BUILD_TIME="$(date '+%Y-%m-%d_%H:%M')"
echo "build stamp: $STAMP ($BUILD_TIME)"
printf '%s\n' "$STAMP" > dist/rg35xx/buildstamp.txt
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "-s -w -X main.buildStamp=$STAMP -X main.buildTime=$BUILD_TIME" \
  -o dist/rg35xx/SDRg35xx/sdrg35xx .

echo "== copying launcher + icon =="
# tr -d '\r' guards against a CRLF checkout (a CRLF shebang kills the
# launcher on the device before it can log).
tr -d '\r' < rg35xx/SDRg35xx.sh > dist/rg35xx/SDRg35xx.sh
chmod +x dist/rg35xx/SDRg35xx.sh
cp rg35xx/SDRg35xx.png dist/rg35xx/SDRg35xx.png

echo "== done =="
echo "package: dist/rg35xx/"
echo "next:    copy SDRg35xx/ + SDRg35xx.sh + SDRg35xx.png to Roms/APPS on"
echo "         the SD card, then launch from the APPS menu."

# Bundled sidecars for the USB dongle source (rtl_tcp, built from
# rtlsdrblog/rtl-sdr-blog; static librtlsdr, links only system
# libusb/libudev) and the USB GPS reader (tools/gpsread.c -- the
# firmware kernel has no cdc_acm/usbserial modules). Users who skip
# the SD-card copy get them via OTA.
for side in rtl_tcp gpsread; do
  if [ -f "dist/rg35xx/$side" ]; then
    cp "dist/rg35xx/$side" "dist/rg35xx/SDRg35xx/$side"
    chmod +x "dist/rg35xx/SDRg35xx/$side"
    echo "== bundled $side sidecar =="
  else
    echo "== WARNING: no $side sidecar found in dist/rg35xx/ =="
  fi
done
