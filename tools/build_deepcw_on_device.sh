#!/bin/sh
# Build the deepcw sidecar directly on the RG35XX (no cross toolchain
# needed): expects third_party/deepcw/{model.onnx,model.onnx.json} and
# the onnxruntime aarch64 libs already on the device (see NOTICE).
set -e
cd "$(dirname "$0")/.."
ORT=third_party/deepcw/ort
gcc -O2 -w -o deepcw tools/deepcw.c \
  -I$ORT/include -L$ORT/lib -l:libonnxruntime.so.1.30.0 -lm
echo built: ./deepcw
