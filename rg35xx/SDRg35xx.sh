#!/bin/bash
# SDRg35xx — RTL-SDR receiver (rtl_tcp client) for Anbernic RG35XX Plus.
# Install: copy SDRg35xx/ (folder), SDRg35xx.sh and SDRg35xx.png into Roms/APPS,
# then rescan/reboot. Launch from the APPS menu.
progdir=$(cd "$(dirname "$0")" && pwd)
appdir="$progdir/SDRg35xx"

# Rotate logs: one .old copy per launch keeps the SD card from filling.
for f in "$progdir/SDRg35xx-logfile.txt"; do
  [ -f "$f" ] && mv -f "$f" "$f.old"
done
exec >"$progdir/SDRg35xx-logfile.txt" 2>&1
echo "=== SDRg35xx launch $(date) ==="
uname -a

# Environment recap into the log — these are the three things bring-up
# depends on (panel format, input device, ALSA cards).
echo "-- fb0:"
for f in /sys/class/graphics/fb0/virtual_size /sys/class/graphics/fb0/bits_per_pixel; do
  echo "  $f = $(cat "$f" 2>/dev/null)"
done
ls -la /dev/fb0 2>/dev/null
echo "-- input devices:"
for n in /sys/class/input/event*/device/name; do
  echo "  $n = $(cat "$n" 2>/dev/null)"
done
echo "-- sound cards:"
cat /proc/asound/cards 2>/dev/null
echo "-- player binaries:"
command -v aplay || echo "  aplay: none"
command -v mpv || echo "  mpv: none"

cd "$appdir"

# ALSA: prefer the speaker codec (card 0) over HDMI when a backend
# resolves "default" (same finding as goro on this firmware).
export ALSA_CARD="${ALSA_CARD:-0}"
# The app context defines neither HOME nor XDG dirs; without HOME the
# config save path falls back beside the binary anyway, but set it for
# consistency with other apps.
export HOME="${HOME:-$appdir}"

# Log survival against hard crashes: sync every 10 s.
(
  while :; do sync; sleep 10; done
) &
syncer=$!

./sdrg35xx "$@"
status=$?
echo "SDRg35xx exited: $status"
sync

# Safety: if the app died with the panel blanked, restore backlight.
for f in /sys/class/backlight/*/bl_power /sys/class/graphics/fb0/blank; do
  [ -w "$f" ] && echo 0 > "$f" 2>/dev/null
done

kill $syncer 2>/dev/null
sync
exit $status
