#!/bin/sh
# Build the ft8ts sidecar bundle (node runtime + ft8ts library + worker
# + sidecar script) that the app fetches lazily on first enable.
# ft8ts is GPL-3.0 (TypeScript port of WSJT-X v3.0.1); node is the
# runtime that executes it. ~30-40 MB gz.
set -e
cd "$(dirname "$0")/.."
STAGE=.webtest/ft8ts-bundle
rm -rf "$STAGE" && mkdir -p "$STAGE"
NODE=.webtest/nodestage/bin/node
if [ ! -f "$NODE" ]; then
  echo "download node-v22.14.0-linux-arm64 first (see third_party/ft8ts/NOTICE)" >&2
  exit 1
fi
cp "$NODE" "$STAGE/ft8ts-node"
# ship the EXACT node binary that was benchmarked on the device —
# a stripped copy was never runtime-tested there, and a broken node
# would kill the feature on first enable
cp third_party/ft8ts/ft8ts.mjs "$STAGE/"
cp third_party/ft8ts/ft8ts-worker-node.mjs "$STAGE/"
cp tools/ft8ts_sidecar.mjs "$STAGE/"
tar czf dist/ft8ts-bundle-linux-arm64.tar.gz -C "$STAGE" \
  ft8ts-node ft8ts.mjs ft8ts-worker-node.mjs ft8ts_sidecar.mjs
ls -la dist/ft8ts-bundle-linux-arm64.tar.gz
