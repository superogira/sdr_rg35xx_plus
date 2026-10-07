#!/bin/sh
# Build the DeepCW sidecar bundle (sidecar + ONNX Runtime libs + model)
# that the app fetches lazily on first enable (~38 MB gz).
# Runs natively where aarch64-linux-gnu-gcc exists; on a Windows dev
# box it delegates the compile to the WSL Ubuntu-22.04 toolchain.
set -e
cd "$(dirname "$0")/.."
ORT=third_party/deepcw/ort
STAGE=.webtest/deepcw-bundle
rm -rf "$STAGE" && mkdir -p "$STAGE"
if command -v aarch64-linux-gnu-gcc >/dev/null 2>&1; then
  aarch64-linux-gnu-gcc -O2 -w -o "$STAGE/deepcw" tools/deepcw.c \
    -I$ORT/include -L$ORT/lib -l:libonnxruntime.so.1.30.0 -lm
else
  # C:\a\b -> /mnt/c/a/b (wslpath only exists inside WSL)
  POSIXDIR=$(pwd | sed -e 's|^/\([a-zA-Z]\)/|/mnt/\1/|')
  wsl -d Ubuntu-22.04 -- bash -c "cd '$POSIXDIR' && aarch64-linux-gnu-gcc -O2 -w \
    -o '$STAGE/deepcw' tools/deepcw.c \
    -I$ORT/include -L$ORT/lib -l:libonnxruntime.so.1.30.0 -lm"
fi
cp $ORT/lib/libonnxruntime.so.1.30.0 "$STAGE/libonnxruntime.so.1"
cp $ORT/lib/libonnxruntime_providers_shared.so "$STAGE/"
cp third_party/deepcw/model.onnx "$STAGE/deepcw-model.onnx"
cp third_party/deepcw/model.onnx.json "$STAGE/deepcw-model.onnx.json"
tar czf dist/deepcw-bundle-linux-arm64.tar.gz -C "$STAGE" \
  deepcw libonnxruntime.so.1 libonnxruntime_providers_shared.so \
  deepcw-model.onnx deepcw-model.onnx.json
ls -la dist/deepcw-bundle-linux-arm64.tar.gz