#!/bin/sh
# Build and publish SDRg35xx to the download server (OTA update source).
#
# Credentials live in ftp.env (gitignored) next to this script:
#   FTP_HOST=192.168.1.211
#   FTP_USER=...
#   FTP_PASS=...
#   FTP_PATH=/httpdocs   (root of downloads domain; adjust if needed)
set -e
cd "$(dirname "$0")"

if [ -f ftp.env ]; then
  . ./ftp.env
fi
: "${FTP_HOST:=192.168.1.211}"
: "${FTP_PATH:=/}"

./build-rg35xx.sh
STAMP="$(cat dist/rg35xx/buildstamp.txt)"
BIN=dist/rg35xx/SDRg35xx/sdrg35xx
PKG=dist/sdrg35xx-linux-arm64.gz

gzip -9 -c "$BIN" > "$PKG"
SHA="$(sha256sum "$PKG" | cut -d' ' -f1)"
SIZE="$(wc -c < "$PKG" | tr -d ' ')"

# sha256 in version.txt is over the GZIPPED package (verify before
# decompress).
printf 'stamp=%s\nsha256=%s\nsize=%s\n' "$STAMP" "$SHA" "$SIZE" > dist/version.txt

echo "== uploading stamp $STAMP ($SIZE bytes gz) =="
curl -sfT "$PKG" --ftp-create-dirs "ftp://$FTP_USER:$FTP_PASS@$FTP_HOST${FTP_PATH}sdrg35xx/sdrg35xx-linux-arm64.gz"
curl -sfT dist/version.txt --ftp-create-dirs "ftp://$FTP_USER:$FTP_PASS@$FTP_HOST${FTP_PATH}sdrg35xx/version.txt"

echo "== done =="
echo "http://downloads.catgg.net/sdrg35xx/version.txt"
