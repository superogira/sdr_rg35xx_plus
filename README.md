# SDRg35xx — RTL-SDR receiver for Anbernic RG35XX (Plus/H/2024)

An SDR receiver app for the Anbernic RG35XX Plus handheld (H700, Linux
arm64), written in Go. It takes IQ from an **RTL-SDR dongle plugged into
the device's own USB port** or from a **network rtl_tcp server**, then
demodulates audio, draws a waterfall, decodes digital modes, and serves a
LAN web control panel.

![SDRg35xx](dist/rg35xx/SDRg35xx.png)

## Features

### Reception & display
- Receive modes: WFM (broadcast FM), NFM, AM, USB, LSB, CW
- Waterfall + spectrum on the device screen (span zoom L1/R1, tuning step
  L2/R2) and in the browser (touch/drag/pinch, fullscreen) — full-screen
  scrollable history windows for FT8 (SELECT), CW with both decoders side
  by side (SELECT), AIS and APRS
- S-meter (dBFS + S-units) on screen and web; absolute-dBFS squelch
- Screen brightness via MENU+Vol; dim/blank from the power button or the web
- Screenshots with MENU+START

### Digital mode decoding
- **FT8** — world map with 21 views, PSK Reporter spot uploads, log +
  station detail
- **Neural CW (AI)** — a neural-network Morse decoder (DeepCW) running as a
  separate sidecar; independent of the classic CW decoder, tunable
  threads/window length, very strong on weak and noisy signals (decodes at
  −6 dB SNR); text shown on screen (mini window + full-screen split view of
  both decoders, scrollable history) and on the web
- **Alternate FT8 engine (ft8ts)** — a TypeScript port of the WSJT-X v3.0.1
  FT8/FT4 decoder running as a separate node sidecar; can run alongside or
  instead of the built-in one; depth 1–3 / threads / audio band settings;
  its decodes merge into the same log/map/web/PSK Reporter feeds, tagged
  with their source (app/alt)
- **RTTY** (Baudot 45.45), **CW** (self-learning speed 12–45 wpm),
  **WEFAX/HF-FAX** (auto image save, adjustable line start),
  **SSTV** (Martin M1/M2, Scottie S1/S2, PD90/120/180/240, Robot 36C/72C —
  VIS auto-detect; the modes used by ISS/ARISS space stations)
- **AIS** from NMEA feeds and decoded directly off the RF (both channels
  ±25 kHz)
- **ADS-B radar** — Beast feed, 6 OSM/satellite basemap layers, country
  flags, altitude-coloured breadcrumbs, per-aircraft detail view
- **In-app ADS-B from the RF** — decodes Mode S itself from raw IQ at
  2.4 MSPS (algorithm ported from readsb's `demod_2400.c`: preamble
  pre-check, 5-phase slice correlators, CRC-24); no external
  dump1090/readsb needed; enabling it retunes to 1090 MHz and sets 2.4M
  automatically, restoring your previous settings on disable; results join
  the same store as the Beast feed (radar/web see them together)
- **IQ sharing (rtl_tcp server)** — the app can run its own rtl_tcp server
  (configurable port, default 1235) so other hosts on the LAN point at the
  handheld's IP and receive the very same IQ stream (SDRSharp/gqrx/tar1090);
  remote clients cannot retune the dongle (the app owns it), and clients
  that fall behind are dropped so they never block reception

### GPS + APRS
- GPS over USB (NMEA/ttyACM, or the libusb helper `gpsread` when the kernel
  lacks cdc_acm) — live info screen, automatic device-clock setting, used
  as the radar/web position
- **Full APRS**: decodes 1200 baud AFSK off the RF (in any receive mode)
  onto the radar with the sender's symbol + country flag; **transmits
  beacons** as AFSK audio out of the speaker (patch into a VOX-keyed radio)
  in off / every 1–30 min / **SmartBeaconing** (speed-based rate + corner
  pegging) modes; **position source selectable**: live GPS or a fixed
  entered position (base stations without GPS); optional simultaneous
  **APRS-IS** posting; **iGate** (off by default) forwards RF-heard stations
  to APRS-IS with qAR rules, loop protection and a per-minute cap; 3-tab
  RX/TX/station history screen

### Web control (LAN)
- Full control: tune (click the frequency for a modal), mode/BW/gain/
  volume/squelch/ppm, AGC, audio filters, NR/HP/LP, sample rate,
  FT8/AIS RF/**ADS-B RF**/**IQ share (rtl_tcp server)**
- Full-rate spectrum + waterfall, frequency axis, passband brackets, live
  S-meter and squelch state
- Map: aircraft/ships/APRS stations as real rotated silhouettes,
  altitude-coloured flight trails, ship tracks, **FT8 stations + direction-
  animated QSO arcs** (alternating dashes + ripple rings while transmitting),
  country flags, detail popups, fullscreen map mode (`?map=1`), separate
  toggles for target kinds and labels/flags
- The web waterfall uses screen-resolution max-hold when a pixel covers
  several bins (thin lines stay visible), plus a crosshair with a
  cursor-following frequency readout
- System card: CPU/MEM/BAT (+charging)/temperatures, GPS summary + full
  modal, screen dim/off/on buttons, system update
- Listen to the handheld's audio in the browser (µ-law stream)

### System
- 9 menu pages grouped by task (Receive & Tune / Audio / Display / Radar &
  Targets / GPS / APRS / CW & Digital Modes / Station & Reports / System)
  plus a bookmarks row on the root page; bilingual Thai/English UI
- Frequency bookmarks, log viewer, system monitor
- Self-update over OTA (menu or web button)

## Installation

Copy onto the handheld's SD card:

```
dist/rg35xx/SDRg35xx/   →  Roms/APPS/SDRg35xx/
dist/rg35xx/SDRg35xx.sh →  Roms/APPS/
dist/rg35xx/SDRg35xx.png → Roms/APPS/
```

then launch from the device's **APPS** menu. Updates come from System →
"Check update" (OTA) without removing the card.

> ⚠️ **Launch from the APPS menu only** — never run it alongside the
> launcher (dmenu) that is currently displaying: both would fight over the
> framebuffer (fb0) and audio and lock the whole device. Over SSH, run
> `pkill -x dmenu.bin` first (the launcher screen stays dark until reboot).

## Using an RTL-SDR on the device's USB (no computer)

Plug an RTL2832U dongle (RTL-SDR Blog V3/V4 or generic 22–1100 MHz) into
the RG35XX USB port, then open the menu:

**Receive & Tune → Source → "USB — dongle in device"**

The app spawns its own bundled `rtl_tcp` (built from
[rtlsdrblog/rtl-sdr-blog](https://github.com/rtlsdrblog/rtl-sdr-blog) —
V4 support plus the common FC0013/E4000/R820T/FC0012/FC2580 tuners) bound
to `127.0.0.1:1234` and connects automatically — nothing else to install
(the stock firmware already carries the needed libusb/libudev). Leaving the
app or switching back to a TCP source releases the dongle immediately. A
powered USB hub runs the dongle and a GPS at the same time.

Notes:

- First-time OTA installs download `rtl_tcp` once during the update; if
  that fails, copy `dist/rg35xx/SDRg35xx/rtl_tcp` from the repo into the
  app folder on the card
- HF reception via the V3's direct sampling is not supported in USB mode
  yet (use a TCP server with the Q branch enabled instead)
- To run your own `rtl_tcp` (e.g. shared with the LAN), start
  `rtl_tcp -a 0.0.0.0` on the device and pick host `device-IP:1234` as usual

### Driver / DVB-T blacklist

The RG35XX firmware (Ubuntu-based) does **not** autoload the DVB-T TV
driver (`dvb_usb_rtl28xxu`) when a dongle is plugged in, so nothing is
needed. On other firmware/distros where the device is invisible or `lsusb`
sees it but rtl_tcp cannot open it, blacklist it the standard way per
[rtl-sdr.com](https://www.rtl-sdr.com/v4/) by creating
`/etc/modprobe.d/blacklist-rtl.conf`:

```
blacklist dvb_usb_rtl28xxu
blacklist rtl2832
blacklist rtl2838
```

then reboot. The V4 still needs rtl-sdr-blog's librtlsdr (bundled with the
app as the `rtl_tcp` file).

## Using rtl_tcp over the network (as before)

Run rtl_tcp on any computer/server and add its `IP:port` to the app's
source list — ideal for a computer next to the antenna or for HF via direct
sampling. Note: a jittery internet path drops samples in bursts and hurts
FT8 decoding (see the "sample drop detected" log lines); prefer LAN at home.

## Transmitting APRS through an existing radio

Patch a cable from the RG35XX headphone jack into the radio's mic/line-in,
enable VOX, then configure in the **APRS** menu: callsign (with SSID),
beacon mode, AFSK level, and a preamble longer than your radio's VOX
open time (0.1–2.0 s). While transmitting, the app mutes receive audio
automatically and resumes it smoothly afterwards.

## Building from source

```
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build .
```

or `./build-rg35xx.sh` for the packaged icon/launcher bundle and
`./upload.sh` to publish an OTA update (configure `ftp.env` first).

The `rtl_tcp` in `dist/rg35xx/` is built from rtl-sdr-blog sources with a
static librtlsdr (linking only the system libusb/libudev) — see the comment
at the top of `build-rg35xx.sh`; `gpsread` is built from `tools/gpsread.c`
(libusb) for GPS units whose kernel has no driver.

## Credits & thanks

This project stands on the shoulders of much excellent open-source work —
thank you to every developer who published the code and tools that made
this app possible:

| Project | Author | Used for | License |
|---|---|---|---|
| [rtl-sdr-blog](https://github.com/rtlsdrblog/rtl-sdr-blog) | RTL-SDR Blog (rtlsdrblog) | the `rtl_tcp` sidecar for the USB dongle | GPL-2.0 |
| [readsb](https://github.com/wiedehopf/readsb) | wiedehopf | the Mode S/ADS-B demodulation algorithm (`demod_2400.c`) ported into the app | GPL-2.0+ |
| [ft8_lib](https://github.com/kgoba/ft8_lib) | Karlis Goba (KG7NAB / kgoba) | LDPC/CRC tables of the built-in FT8 decoder | MIT/BSL (see file) |
| [AIS-catcher](https://github.com/jvde-github/AIS-catcher) | jvde-github | the GMSK demodulator architecture ported for AIS RF | GPL-3.0 |
| [ft8ts](https://github.com/e04/ft8ts) | e04 | the alternate FT8/FT4 decoder (TypeScript port of WSJT-X v3.0.1) | GPL-3.0 |
| [DeepCW / deepcw-engine](https://github.com/e04/deepcw-engine) | e04 | the ONNX model + reference inference of the neural CW decoder | AGPL-3.0-only |
| [HamNoise](https://github.com/e04/HamNoise) | e04 | the neural noise-reduction sidecar (voice/CW) | AGPL-3.0 |
| [ONNX Runtime](https://github.com/microsoft/onnxruntime) | Microsoft | runtime executing the DeepCW model in the sidecar | MIT |
| [Node.js](https://github.com/nodejs/node) | Node.js contributors / OpenJS Foundation | runtime executing the ft8ts sidecar | Node.js License |
| [WSJT-X](https://wsjt.sourceforge.io/) | Joe Taylor (K1JT) and team | the original FT8/FT4 algorithms ft8ts ports | GPL-3.0 |
| [Leaflet](https://leafletjs.com/) | Vladimir Agafonkin and contributors | the web map | BSD-2 |
| [OpenStreetMap / tile providers](https://www.openstreetmap.org/copyright) | OSM contributors | radar basemaps (OSM/OpenTopoMap/ESRI) | ODbL / provider terms |
| [country-flag-icons](https://github.com/catamphetamine/country-flag-icons) | catamphetamine | country flag icons | MIT |
| [Sarabun](https://github.com/cadsondemak/Sarabun) | Cadson Demak | the Thai font on the device screen | OFL-1.1 |
| [libusb](https://libusb.info/) | libusb contributors | the `gpsread` sidecar for driverless GPS units | LGPL-2.1 |
| [tar1090](https://github.com/wiedehopf/tar1090) | wiedehopf | inspiration for the ADS-B radar colours and UI conventions | GPL-3.0 |

Special thanks: the amateur-radio community whose real on-air signals are
tested against every day, the maintainers of every tile server that allows
free use, and everyone who reported bugs from the field — your logs and
symptom descriptions are what keep making this handheld decode better.

Full license texts for each upstream work live in the
[`Licenses/`](Licenses) folder (and `third_party/*/LICENSE`, `NOTICE` for
vendored parts).

## License

**SDRg35xx is licensed under the GNU General Public License v3.0 or any
later version (GPL-3.0-or-later)** — see [LICENSE](LICENSE). Every source
file carries an SPDX header; the two files that port copyleft algorithms
(`internal/adsb/modes.go` from readsb, `internal/ais/gmsk.go` from
AIS-catcher) state their upstream provenance in the header as well.

GPL is not merely a preference here: the Mode S demodulator ports readsb's
algorithm and coefficient tables (GPL v3+), and the AIS RF demodulator
ports AIS-catcher's ModelDefault architecture (GPL-3.0). Both are compiled
into the main binary, so copyleft covers the combined work — distributing
the binary (OTA, SD card) obliges the whole program to GPL. The
MIT-licensed ft8_lib tables and the permissively licensed dependencies are
compatible with that.

Components that run as SEPARATE processes keep their own licenses and never
link into the binary:

| Component | License | Integration |
|---|---|---|
| the app `sdrg35xx` (incl. the readsb/AIS-catcher/ft8_lib ports) | GPL-3.0-or-later | single binary |
| `rtl_tcp` (rtlsdrblog/rtl-sdr-blog) | GPL-2.0 | sidecar process |
| `hamnoise` (e04/HamNoise) | AGPL-3.0 | sidecar process |
| `deepcw` + ONNX Runtime + DeepCW model (e04/deepcw-engine) | AGPL-3.0-only / MIT / AGPL-3.0-only | sidecar process |
| `ft8ts` + Node.js (e04/ft8ts) | GPL-3.0 / Node.js License | sidecar process |

---

# ฉบับภาษาไทย

แอปรับสัญญาณวิทยุ SDR บนเครื่องเกม Anbernic RG35XX Plus (H700 / linux arm64)
เขียนด้วย Go — รับ IQ จาก **RTL-SDR ผ่าน USB ของเครื่องโดยตรง** หรือจาก
**rtl_tcp server ทางเครือข่าย** แล้วถอดสัญญาณฟังเสียง + แสดงน้ำตก +
ถอดรหัสดิจิทัล พร้อมเว็บควบคุมผ่าน LAN

## ความสามารถ

### รับสัญญาณและจอภาพ
- โหมดรับ: WFM (วิทยุ FM), NFM, AM, USB, LSB, CW
- น้ำตก + สเปกตรัม บนจอเครื่อง (ซูม span ด้วย L1/R1, สเต็ปจูน L2/R2)
  และบนเว็บ (แตะ/ลาก/ซูม 2 นิ้ว, ขยายเต็มจอ) — หน้าต่างประวัติเต็มจอแบบ
  เลื่อนดูได้สำหรับ FT8 (SELECT), CW สองตัวถอดแบ่งครึ่งจอ (SELECT), AIS และ APRS
- S-meter (dBFS + S-unit) ทั้งบนจอเครื่องและเว็บ, squelch แบบ absolute dBFS
- ปรับความสว่างจอด้วย MENU+Vol, ปิด/หรี่จอจากปุ่ม power หรือจากเว็บ
- ถ่ายภาพหน้าจอด้วย MENU+START

### ถอดรหัสดิจิทัล
- **FT8** — แผนที่โลก 21 แบบ, ส่งรายงาน PSK Reporter, log + รายละเอียดสถานี
- **CW นิวรัล (AI)** — ตัวถอดมอร์สแบบ neural network (DeepCW) เป็น
  sidecar แยก เปิด/ปิดอิสระจากตัวถอด CW เดิม, ตั้งเธรด/ความยาวหน้าต่างได้,
  ทนสัญญาณอ่อนและ noise สูงมาก (ถอดได้ที่ SNR −6 dB), ข้อความแสดงทั้งบนจอ
  (หน้าต่างเล็ก + หน้าต่างใหญ่แบ่งครึ่งสองตัวถอด เลื่อนย้อนหลังได้) และบนเว็บ
- **FT8 สำรอง (ft8ts)** — ตัวถอด FT8/FT4 พอร์ตของ WSJT-X v3.0.1 เป็น sidecar
  node แยก เปิดคู่หรือแทนตัวเดิมได้, ตั้ง depth 1–3 / threads / ย่านเสียง,
  ผลถอดรวมเข้า log/แผนที่/เว็บ/PSK Reporter เดียวกันพร้อมป้ายแหล่งถอด (app/alt)
- **RTTY** (Baudot 45.45), **CW** (ความเร็วเรียนรู้เอง 12–45 wpm),
  **WEFAX/HF-FAX** (บันทึกรูปอัตโนมัติได้, ขยับจุดเริ่มแถวภาพได้),
  **SSTV** (Martin M1/M2, Scottie S1/S2, PD90/120/180/240, Robot 36C/72C —
  VIS auto-detect; โหมดที่สถานีอวกาศ ISS/ARISS ใช้)
- **AIS** ทั้งจาก NMEA feed และถอดจากคลื่น RF โดยตรง (2 ช่อง ±25 kHz)
- **ADS-B radar** — Beast feed, แผนที่ OSM/ดาวเทียม 6 เลเยอร์, ธงประเทศ,
  breadcrumb ตามความสูง, เลือกสถานีดูรายละเอียด
- **ADS-B จากคลื่นในตัวแอป (RF)** — ถอด Mode S เองจาก IQ ดิบที่ 2.4 MSPS
  (พอร์ตอัลกอริทึมจาก readsb `demod_2400.c`: preamble pre-check, slice
  correlators 5 เฟส, CRC-24) ไม่ต้องมี dump1090/readsb ภายนอก; เปิดแล้วแอป
  จูน 1090 MHz + ตั้งอัตรา 2.4M เอง แล้วคืนค่าเดิมตอนปิด; ผลเข้า store เดียวกับ
  Beast feed (เรดาร์/เว็บเห็นรวมกัน)
- **แชร์ IQ ให้เครื่องอื่น (rtl_tcp server)** — เปิดเซิร์ฟเวอร์ rtl_tcp ในตัวแอป
  (พอร์ตตั้งได้, ค่าเริ่มต้น 1235) เครื่องอื่นใน LAN ชี้มาที่ IP เครื่องเกมแล้วรับ
  สตรีม IQ เดียวกันได้เลย (SDRSharp/gqrx/tar1090); ไคลเอนต์ปรับจูนไม่ได้
  (ดองเกิลเป็นของแอป) และไคลเอนต์ที่ตามไม่ทันจะถูกตัดเพื่อไม่ให้บล็อกการรับ

### GPS + APRS
- รับ GPS ผ่าน USB (NMEA/ttyACM หรือ libusb helper `gpsread` เมื่อเคอร์เนล
  ไม่มี cdc_acm) — หน้าจอข้อมูลสด, ตั้งนาฬิกาเครื่องอัตโนมัติ, ใช้เป็น
  ตำแหน่งเรดาร์/เว็บ
- **APRS ครบวงจร**: ถอด AFSK 1200 baud จากคลื่น (ทุกโหมดรับ) แสดงบนเรดาร์
  ด้วยสัญลักษณ์ที่ผู้ส่งตั้ง + ธงประเทศ; **ส่ง beacon** ออกเสียง AFSK ทาง
  ลำโพง (ต่อวิทยุ + VOX) โหมด off / ทุก 1–30 นาที / **SmartBeaconing**
  (เร็วส่งถี่ + corner pegging); **แหล่งตำแหน่งเลือกได้**: GPS สด หรือ
  พิกัดคงที่ที่กรอกเอง (สถานีฐานไม่มี GPS); โพสต์ **APRS-IS** ได้คู่กัน;
  **iGate** (ปิดเป็นค่าเริ่มต้น) ส่งต่อสถานีที่รับจาก RF ขึ้น APRS-IS ตาม
  กติกา qAR + กันลูป + เพดานต่อนาที; จอประวัติรับ/ส่ง/สถานี 3 แท็บ

### เว็บควบคุม (LAN)
- ควบคุมครบ: จูน (กดเลขความถี่เปิด modal), โหมด/BW/gain/volume/squelch/ppm,
  AGC, ตัวกรองเสียง, NR/HP/LP, sample rate, FT8/AIS RF/**ADS-B RF**/
  **IQ Share (rtl_tcp server)**
- สเปกตรัม + น้ำตกความละเอียดเต็มอัตรา, แกนความถี่, ปีกกา passband,
  S-meter + สถานะ squelch สด
- แผนที่: เครื่องบิน/เรือ/สถานี APRS เป็นไอคอนหมุนตามทิศจริง, เส้นทางบิน
  สีตามความสูง, track เรือ, **สถานี FT8 + เส้น QSO โค้งวิ่งตามทิศ** (ขีดสี
  สลับต่อขีด + วง ripple ตอนสถานีส่ง), ธงประเทศ, popup รายละเอียด,
  โหมดแผนที่เต็มจอ (`?map=1`), toggle ชนิดเป้าและป้ายชื่อ/ธงแยกกัน
- น้ำตกเว็บใช้ max-hold ความละเอียดจอเมื่อพิกเซลครอบหลาย bin (เส้นบางไม่จม)
  และ crosshair + ป้ายความถี่ตาม cursor
- การ์ดระบบ: CPU/MEM/BAT (+สถานะชาร์จ)/อุณหภูมิ, GPS สรุป+modal เต็ม,
  ปุ่มหรี่/ปิด/เปิดจอ, อัปเดตระบบ
- ฟังเสียงจากเครื่องเกมในเบราว์เซอร์ (µ-law stream)

### ระบบ
- เมนู 9 หมวดจัดตามกลุ่มงาน (การรับและจูน / เสียง / การแสดงผล / เรดาร์และเป้าหมาย /
  GPS / APRS / CW และโหมดดิจิทัล / สถานีและรายงาน / ระบบ) + แถวรายการความถี่
  บนหน้าราก สองภาษา ไทย/อังกฤษ
- รายการความถี่ (bookmarks), log viewer, จอระบบ
- อัปเดตตัวเองผ่าน OTA (เมนูหรือปุ่มบนเว็บ)

## การติดตั้ง

คัดลอกลงการ์ด SD ของเครื่อง:

```
dist/rg35xx/SDRg35xx/   →  Roms/APPS/SDRg35xx/
dist/rg35xx/SDRg35xx.sh →  Roms/APPS/
dist/rg35xx/SDRg35xx.png → Roms/APPS/
```

แล้วเปิดจากเมนู **APPS** ของเครื่อง อัปเดตเวอร์ชันใหม่ทำได้จากเมนู
ระบบ → "ตรวจอัปเดต" (OTA) ไม่ต้องถอดการ์ด

> ⚠️ **เปิดจากเมนู APPS เท่านั้น** — ห้ามรันแอบไปพร้อม launcher
> (dmenu) ที่กำลังแสดงผลอยู่ ทั้งสองโปรแกรมจะแย่งจอ (fb0) และเสียง
> ทำให้เครื่องค้างทั้งเครื่อง ถ้าจะรันผ่าน SSH ให้ `pkill -x dmenu.bin`
> ก่อน (จอ launcher จะดับไปจนกว่าจะรีบูต)

## ใช้ RTL-SDR เสียบ USB ของเครื่อง (ไม่ต้องมีคอมพิวเตอร์)

เสียบ dongle RTL2832U (เช่น RTL-SDR Blog V3/V4, หรือรุ่น generic
22–1100 MHz) เข้าช่อง USB ของ RG35XX แล้วเข้าเมนู

**การรับและจูน → แหล่งสัญญาณ → "USB — dongle ในเครื่อง"**

แอปจะเรียก `rtl_tcp` ที่แนบมากับตัวแอปเอง (สร้างจาก
[rtlsdrblog/rtl-sdr-blog](https://github.com/rtlsdrblog/rtl-sdr-blog) —
รองรับ V4 + tuner รุ่นทั่วไป FC0013/E4000/R820T/FC0012/FC2580) ผูกกับ
`127.0.0.1:1234` แล้วเชื่อมต่อเองโดยอัตโนมัติ — ไม่ต้องติดตั้งอะไรเพิ่ม
(firmware ต้นฉบับมี libusb/libudev ที่ต้องใช้อยู่แล้ว) ตอนออกจากแอป
หรือสลับกลับไปใช้ TCP dongle จะถูกปล่อยให้โปรแกรมอื่นใช้ได้ทันที
USB hub ที่มีไฟเลี้ยงต่อได้ทั้ง dongle และ GPS พร้อมกัน

หมายเหตุ:

- ผู้ที่ติดตั้งแอปผ่าน OTA ครั้งแรก แอปจะดาวน์โหลดไฟล์ `rtl_tcp`
  ให้เองหนึ่งครั้งระหว่างอัปเดต ถ้าดาวน์โหลดไม่สำเร็จให้คัดลอก
  `dist/rg35xx/SDRg35xx/rtl_tcp` จาก repo ไปไว้ในโฟลเดอร์แอปบนการ์ด
- ยังไม่รองรับรับคลื่น HF ผ่าน direct sampling ของ V3 ในโหมด USB
  (ใช้ TCP server ที่เปิด Q-branch แทนได้)
- อยากรัน `rtl_tcp` เอง (เช่น เปิดให้เครื่องอื่นใน LAN ใช้) —
  รัน `rtl_tcp -a 0.0.0.0` ในเครื่อง แล้วเลือกใช้ host
  `IPของเครื่อง:1234` ตามปกติ

### Driver / DVB-T blacklist

Firmware ของ RG35XX (พื้นฐาน Ubuntu) **ไม่ได้**โหลด driver DVB-T TV
(`dvb_usb_rtl28xxu`) อัตโนมัติเมื่อเสียบ dongle จึงไม่ต้องทำอะไร
แต่ถ้าใช้ firmware/distro อื่นที่มีอาการมองไม่เห็นอุปกรณ์ หรือ
`lsusb` เห็นแต่ rtl_tcp เปิดไม่ได้ ให้ blacklist ตามวิธีมาตรฐานจาก
[rtl-sdr.com](https://www.rtl-sdr.com/v4/) โดยสร้างไฟล์
`/etc/modprobe.d/blacklist-rtl.conf`:

```
blacklist dvb_usb_rtl28xxu
blacklist rtl2832
blacklist rtl2838
```

แล้วรีบูต สำหรับ V4 ยังต้องใช้ librtlsdr จาก rtl-sdr-blog (แนบมากับ
แอปแล้วในไฟล์ `rtl_tcp`)

## ใช้ rtl_tcp ผ่านเครือข่าย (เหมือนเดิม)

รัน rtl_tcp บนคอม/เซิร์ฟเวอร์ใดก็ได้ แล้วเพิ่ม host `IP:พอร์ต` ใน
รายการแหล่งสัญญาณของแอป เหมาะกับกรณีวางคอมไว้ข้างเสาอากาศ
หรือรับ HF ผ่าน direct sampling — หมายเหตุ: เส้นทางอินเทอร์เน็ตที่
แกว่งจะทำให้ตัวอย่างหายเป็นช่วงและกระทบการถอด FT8 (ดู log
"sample drop detected"); ในบ้านควรใช้ LAN

## ส่ง APRS ผ่านวิทยุที่มีอยู่

ต่อสายจากช่องหูฟังของ RG35XX เข้าช่องไมค์/line-in ของวิทยุ เปิด VOX
แล้วตั้งในเมนู **APRS**: สัญญาณเรียก (พร้อม SSID), โหมด beacon,
ระดับเสียง AFSK และเวลา preamble ให้ยาวกว่าเวลาเปิดของ VOX วิทยุ
(0.1–2.0 วินาที) — ระหว่างส่ง แอปจะตัดเสียงรับอัตโนมัติและกลับมา
แบบนุ่มนวลหลังส่งจบ

## Build จากซอร์ส

```
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build .
```

หรือ `./build-rg35xx.sh` เพื่อสร้างแพ็กเกจพร้อม icon/launcher และ
`./upload.sh` เพื่อเผยแพร่ OTA (ตั้งค่า `ftp.env` ก่อน)

ไฟล์ `rtl_tcp` ใน `dist/rg35xx/` build จากซอร์ส rtl-sdr-blog แบบ
static librtlsdr (link แค่ libusb/libudev ของระบบ) — ดูขั้นตอนใน
คอมเมนต์ต้นไฟล์ `build-rg35xx.sh`; `gpsread` build จาก
`tools/gpsread.c` (libusb) สำหรับ GPS ที่เคอร์เนลไม่มี driver

## เครดิตและคำขอบคุณ

โปรเจกต์นี้ยืนอยู่บนบ่าของงานโอเพนซอร์สที่ยอดเยี่ยมจำนวนมาก — ขอบคุณผู้พัฒนา
ทุกท่านที่เผยแพร่งานและเครื่องมือที่ทำให้แอปนี้เป็นไปได้:

| งาน | ผู้พัฒนา | ใช้ทำอะไร | License |
|---|---|---|---|
| [rtl-sdr-blog](https://github.com/rtlsdrblog/rtl-sdr-blog) | RTL-SDR Blog (rtlsdrblog) | `rtl_tcp` sidecar สำหรับ dongle USB | GPL-2.0 |
| [readsb](https://github.com/wiedehopf/readsb) | wiedehopf | อัลกอริทึมถอด Mode S/ADS-B (`demod_2400.c`) ที่พอร์ตมาในตัวแอป | GPL-2.0+ |
| [ft8_lib](https://github.com/kgoba/ft8_lib) | Karlis Goba (KG7NAB / kgoba) | ตาราง LDPC/CRC ของตัวถอด FT8 ในแอป | MIT/BSL (ดูไฟล์) |
| [AIS-catcher](https://github.com/jvde-github/AIS-catcher) | jvde-github | สถาปัตยกรรม GMSK demod ของ AIS RF ที่พอร์ตมา | GPL-3.0 |
| [ft8ts](https://github.com/e04/ft8ts) | e04 | ตัวถอด FT8/FT4 สำรอง (พอร์ต TypeScript ของ WSJT-X v3.0.1) | GPL-3.0 |
| [DeepCW / deepcw-engine](https://github.com/e04/deepcw-engine) | e04 | โมเดล ONNX + ตัวอย่าง inference ของตัวถอด CW นิวรัล | AGPL-3.0-only |
| [HamNoise](https://github.com/e04/HamNoise) | e04 | Neural noise reduction sidecar (voice/CW) | AGPL-3.0 |
| [ONNX Runtime](https://github.com/microsoft/onnxruntime) | Microsoft | runtime รันโมเดล DeepCW ใน sidecar | MIT |
| [Node.js](https://github.com/nodejs/node) | Node.js contributors / OpenJS Foundation | runtime รัน sidecar ft8ts | Node.js License |
| [WSJT-X](https://wsjt.sourceforge.io/) | Joe Taylor (K1JT) และทีม | ต้นฉบับอัลกอริทึม FT8/FT4 ที่ ft8ts พอร์ตมา | GPL-3.0 |
| [Leaflet](https://leafletjs.com/) | Vladimir Agafonkin และผู้ร่วมพัฒนา | แผนที่บนเว็บ | BSD-2 |
| [OpenStreetMap / ไทล์ผู้ให้บริการ](https://www.openstreetmap.org/copyright) | ผู้ร่วมสมทบ OSM | แผนที่พื้นหลังเรดาร์ (OSM/OpenTopoMap/ESRI) | ODbL / เงื่อนไขผู้ให้บริการ |
| [country-flag-icons](https://github.com/catamphetamine/country-flag-icons) | catamphetamine | ไอคอนธงประเทศ | MIT |
| [Sarabun](https://github.com/cadsondemak/Sarabun) | Cadson Demak | ฟอนต์ไทยบนจอเครื่อง | OFL-1.1 |
| [libusb](https://libusb.info/) | libusb contributors | `gpsread` sidecar สำหรับ GPS ที่เคอร์เนลไม่มี driver | LGPL-2.1 |
| [tar1090](https://github.com/wiedehopf/tar1090) | wiedehopf | แรงบันดาลใจ/แบบแผนสีและ UI ของจอเรดาร์ ADS-B | GPL-3.0 |

ขอบคุณเป็นพิเศษ: ชุมชนวิทยุสมัครเล่นที่ส่งสัญญาณจริงให้ทดสอบทุกวัน,
ผู้ดูแล tile server ทุกแห่งที่อนุญาตให้ใช้ฟรี, และทุกคนที่รายงานบั๊กจาก
สนามจริง — log และคำบรรยายอาการของคุณคือสิ่งที่ทำให้อุปกรณ์พกพาเครื่องนี้
ถอดรหัสได้แม่นขึ้นเรื่อย ๆ

ข้อความ license ฉบับเต็มของแต่ละงานอยู่ในโฟลเดอร์ [`Licenses/`](Licenses)
(และ `third_party/*/LICENSE`, `NOTICE` สำหรับส่วนที่ vendored)

## License

**SDRg35xx ใช้สัญญาอนุญาต GNU General Public License v3.0 หรือใหม่กว่า
(GPL-3.0-or-later)** — ดู [LICENSE](LICENSE) ไฟล์ซอร์สทุกไฟล์มีหัว SPDX;
สองไฟล์ที่พอร์ตอัลกอริทึม copyleft (`internal/adsb/modes.go` จาก readsb,
`internal/ais/gmsk.go` จาก AIS-catcher) ระบุที่มาต้นทางไว้ในหัวไฟล์ด้วย

GPL ที่นี่ไม่ใช่แค่ความชอบ: ตัวถอด Mode S พอร์ตอัลกอริทึมและตารางสัมประสิทธิ์
ของ readsb (GPL v3+) และตัวถอด AIS RF พอร์ตสถาปัตยกรรม ModelDefault ของ
AIS-catcher (GPL-3.0) ทั้งสองคอมไพล์เข้าไบนารีหลัก copyleft จึงคลุมงานรวม —
การแจกไบนารี (OTA, การ์ด SD) ผูกทั้งโปรแกรมเข้า GPL; ตาราง ft8_lib ที่เป็น
MIT และ dependencies แบบ permissive เข้ากันได้กับเงื่อนไขนี้

ส่วนประกอบที่รันเป็นโปรเซสแยกคง license ของตัวเองและไม่ลิงก์เข้าไบนารี:

| ส่วน | License | รูปแบบการรวม |
|---|---|---|
| ตัวแอป `sdrg35xx` (รวมพอร์ต readsb/AIS-catcher/ft8_lib) | GPL-3.0-or-later | ไบนารีเดียว |
| `rtl_tcp` (rtlsdrblog/rtl-sdr-blog) | GPL-2.0 | sidecar process |
| `hamnoise` (e04/HamNoise) | AGPL-3.0 | sidecar process |
| `deepcw` + ONNX Runtime + โมเดล DeepCW (e04/deepcw-engine) | AGPL-3.0-only / MIT / AGPL-3.0-only | sidecar process |
| `ft8ts` + Node.js (e04/ft8ts) | GPL-3.0 / Node.js License | sidecar process |

ข้อความ license ฉบับเต็มของงาน third-party ทั้งหมดอยู่ในโฟลเดอร์
[`Licenses/`](Licenses) และ `third_party/*/LICENSE` + `NOTICE` สำหรับ
ส่วนที่ vendored ไว้ใน repo