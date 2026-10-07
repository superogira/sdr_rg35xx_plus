#!/bin/sh
# Build the hamnoise sidecar ON the RG35XX (native gcc, like gpsread)
# and pull the binary back to dist/rg35xx/hamnoise.
# Usage: sh tools/build_hamnoise_on_device.sh [host]
# HamNoise core is vendored under third_party/hamnoise (AGPL-3.0).
set -e
HOST="${1:-192.168.2.135}"
cd "$(dirname "$0")/.."
rm -rf /tmp/hnbuild && mkdir -p /tmp/hnbuild
cp -r third_party/hamnoise/core /tmp/hnbuild/core
cp tools/hamnoise.c /tmp/hnbuild/
python - <<'PY'
import paramiko, os
c = paramiko.SSHClient(); c.set_missing_host_key_policy(paramiko.AutoAddPolicy())
c.connect('192.168.2.135', username='root', password='root', timeout=30, banner_timeout=30)
sftp = c.open_sftp()
def put(local, remote):
    sftp.put(local, remote)
c.exec_command('rm -rf /root/hnbuild && mkdir -p /root/hnbuild/core/include /root/hnbuild/core/src /root/hnbuild/core/generated')[1].read()
for f in ['include/denoise_audio.h', 'include/denoise_gru.h', 'include/denoise_model.h']:
    put('third_party/hamnoise/core/' + f, '/root/hnbuild/core/' + f)
for f in ['src/denoise_audio.c', 'src/denoise_gru.c']:
    put('third_party/hamnoise/core/' + f, '/root/hnbuild/core/' + f)
for f in ['generated/cw_model_weights.h', 'generated/voice_model_weights.h']:
    put('third_party/hamnoise/core/' + f, '/root/hnbuild/core/' + f)
put('tools/hamnoise.c', '/root/hnbuild/hamnoise.c')
sftp.close()
cmd = ('cd /root/hnbuild && gcc -O2 -mcpu=cortex-a53 -Icore/include -Icore/generated '
       '-o hamnoise hamnoise.c core/src/denoise_audio.c core/src/denoise_gru.c -lm 2>&1 | tail -5; '
       'ls -la hamnoise')
_, o, e = c.exec_command(cmd, timeout=300)
print(o.read().decode(errors='replace'))
print(e.read().decode(errors='replace')[:400])
sftp = c.open_sftp()
os.makedirs('dist/rg35xx', exist_ok=True)
sftp.get('/root/hnbuild/hamnoise', 'dist/rg35xx/hamnoise')
sftp.close()
c.close()
print('pulled dist/rg35xx/hamnoise:', os.path.getsize('dist/rg35xx/hamnoise'), 'bytes')
PY
