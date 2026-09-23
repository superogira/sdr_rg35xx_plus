# SDRg35xx — SDR receiver + FT8 decoder สำหรับ Anbernic RG35XX Plus

แอปรับฟังวิทยุผ่าน RTL-SDR แบบ rtl_tcp client ทำงานบนเครื่อง RG35XX Plus
(H700, linux/arm64) โดยเชื่อมต่อไปยัง rtl_tcp server ผ่าน Wi-Fi —
พร้อม**ถอดรหัส FT8 ในตัว** (LDPC + CRC เต็มรูปแบบตามสเปค WSJT-X)

[English section below](#sdrg35xx--english)

## ฟีเจอร์เด่น

- **Waterfall + ไม้บรรทัดความถี่**: span ซูมได้ 1000 kHz จนถึง 3 kHz
  พร้อมขีดบอกความถี่จากกึ่งกลาง (ขีดที่ 3 และ 5 แสดงค่าความถี่จริง)
- **โหมดรับ 5 โหมด**: NFM / WFM / **USB / LSB / CW** — SSB/CW ใช้ complex
  bandpass เลือก sideband รับ HF ผ่าน direct sampling ได้
- **FT8 decode ในตัว**: STFT waterfall เต็มข้อความ + Costas correlation
  หาสัญญาณอ่อน + LDPC(174,91) + CRC-14 — **ถอดได้พร้อมกันสูงสุด 10
  ข้อความต่อ scan** แสดงในหน้าต่างประวัติ (เลื่อนดูย้อนหลังได้)
- **เมนูตั้งค่าครบ 16 แถว**: ความถี่ (แก้เลขรายหลัก), โหมด, gain ตามตาราง
  V4, squelch, sample rate, bandwidth ต่อโหมด, HF direct sampling, FT8,
  AGC, แก้ host บนจอ (คีย์บอร์ด on-screen), ภาษา ไทย/English, span,
  **สเต็ปจูน** (10 Hz – 100 kHz), วอลุ่ม, ถ่ายภาพหน้าจอ PNG, ตรวจอัพเดท
- **OTA self-update**: เช็คเวอร์ชั่นจากอินเทอร์เน็ตตอนเปิดแอป โหลด+ตรวจ
  sha256+สลับตัวเอง แล้วรีสตาร์ทเข้าเวอร์ชั่นใหม่ทันที
- **ส่ง log วินิจฉัยขึ้น server อัตโนมัติ** (HTTP POST) ให้ดูปัญหาจากทางบ้านได้
- แถบสถานะ: มิเตอร์สัญญาณ, squelch, **CPU/MEM/SWAP** ของเครื่อง

## วิธี build

จากเครื่อง dev (Windows หรือ Linux):

```sh
./build-rg35xx.sh
```

ได้แพ็กเกจใน `dist/rg35xx/`: `SDRg35xx/` (binary linux/arm64 static),
`SDRg35xx.sh` (launcher), `SDRg35xx.png` (icon)

## OTA — ปล่อยอัพเดทผ่านอินเทอร์เน็ต

แอปเช็ค `https://downloads.catgg.net/sdrg35xx/version.txt` ตอนเปิดโปรแกรม
(และจากเมนู "ตรวจอัพเดท") ถ้ามีเวอร์ชั่นใหม่จะโหลด `sdrg35xx-linux-arm64.gz`
ตรวจ sha256 แล้วรีสตาร์ทเข้าเวอร์ชั่นใหม่ ไม่ต้องเสียบ SD card

ปล่อยเวอร์ชั่นใหม่ทำบนเครื่อง dev:

```sh
./upload.sh     # build → gzip → sha256 → FTP ขึ้น server
```

- รหัส FTP อยู่ใน `ftp.env` (**.gitignore ไว้ ห้าม commit**)
- เลขเวอร์ชั่น = เวลา build (YYYYMMDDHHMM) ฝังใน binary อัตโนมัติ
- ปิด auto-check: `sdrg35xx.ini` → `update=off` หรือเปลี่ยน server `updateurl=`

## วิธีติดตั้งลงเครื่อง

1. คัดลอก `SDRg35xx/`, `SDRg35xx.sh`, `SDRg35xx.png` ไปที่ `Roms/APPS/` ของ SD card
2. เชื่อม Wi-Fi ให้เครื่องอยู่วงเดียวกับ rtl_tcp server
3. เปิดจากเมนู APPS (ถ้าไม่เห็น รีสแกน/รีสตาร์ท)

log อยู่ที่ `Roms/APPS/SDRg35xx-logfile.txt` · ค่าตั้งทั้งหมดบันทึกอัตโนมัติที่
`Roms/APPS/SDRg35xx/sdrg35xx.ini` (แก้มือได้ เช่น `host=`, `gain=`, `step=`)

## ปุ่มจอย

| ปุ่ม          | ทำงาน                                                      |
|---------------|------------------------------------------------------------|
| ← / →         | จูน − / + สเต็ป (ตั้งได้ในเมนู 10 Hz – 100 kHz) กดค้าง = ทำซ้ำ |
| ↑ / ↓         | จูน ± 10× สเต็ป                                            |
| A             | สลับโหมด NFM → WFM → USB → LSB → CW                        |
| **SELECT**    | **หน้าต่างประวัติ FT8 ใหญ่** (↑↓ เลื่อนบรรทัด, ←→ เลื่อนหน้า) |
| **Y**         | ทำ marker เวลา FT8 — เส้นแดง+วันเวลาคั่นทุก 15 วิบน waterfall |
| X             | squelch 4→8→12→16→OFF                                      |
| L1 / R1, VOL± | วอลุ่ม − / + ทีละ 1%                                       |
| MENU (สั้น)   | เปิด/ปิดเมนูตั้งค่า                                        |
| MENU/START ค้าง 3 วิ | ออกจากแอป                                          |

## ใช้ FT8

1. จูนย่าน FT8 เช่น **21.074 MHz โหมด USB** (bandwidth 2.6 kHz ปกติ)
2. เมนู → แถว **FT8 Decode** → ON — ตัวหาสัญญาณ sync เองอัตโนมัติ
   (ไม่ต้องกดอะไรเพิ่ม ยกเว้นอยากดู marker เวลาให้กด Y ตอนสัญญาณเริ่ม)
3. ข้อความที่ถอดได้โผล่: หน้าต่างเล็กมุมซ้ายล่าง + แถบสถานะ +
   กด SELECT ดูประวัติเต็ม (เก็บ 100 ข้อความล่าสุด)
4. แต่ละ scan (~1 วิ) ถอดได้พร้อมกันสูงสุด 10 ข้อความ

## สถาปัตยกรรมและเหตุผล

- **Go แบบ pure (CGO_ENABLED=0)** คอมไพล์ข้ามเป็น linux/arm64 จาก Windows ได้เลย
- **เขียน /dev/fb0 ตรง ๆ** (ioctl FBIOGET_VSCREENINFO, double-buffer mirror,
  FBIOPAN activation) — pipeline เรนเดอร์พิสูจน์เสถียรบนเครื่องจริงแล้ว
- **อ่านปุ่มจาก evdev** ผ่าน goroutine เฉพาะ (Go netpoller บล็อก ioctl ได้)
- **เสียง**: DSP ในโปรเซส → resample เป็น 48 kHz → pipe PCM เข้า `aplay`
  (สำรอง `mpv`) — ตามวิธีที่ใช้ได้จริงบน StockOS นี้
- **DSP ล้วน Go**: DC blocker → FIR decimate → ตามโหมด:
  - NFM: ตัวกรองช่องสัญญาณ (decim พร้อมกัน) → polar discriminator → 8 kHz
  - WFM: 127-tap IF → discriminator 32k → de-emphasis
  - SSB/CW: complex bandpass ที่ 8 kHz เลือก sideband + AGC ตัวเลือก
- **FT8 stack** (port จาก kgoba/ft8_lib + WSJT-X packjt77):
  STFT waterfall incremental 93 บล็อก × 2×2 oversampling (FFT 2560 จุด
  mixed-radix 5×512 เขียนเอง เพราะตัวเดิมรับแค่ power-of-2) → หา candidate
  ด้วย soft Costas correlation (140 ตัว) → soft LLR จาก bin →
  LDPC(174,91) belief-propagation + CRC-14 → unpack ข้อความ (callsign
  base-36/27/10, token CQ/DE/QRZ, grid, report, free text) —
  ทดสอบ round-trip ด้วยสัญญาณสังเคราะห์ที่ 0 dB ผ่านทุกกรณี

## ข้อควรรู้เกี่ยวกับ rtl_tcp server (e25wop.thddns.net:2255)

พบจากการทดสอบจริง (ดู `cmd/testlisten`):

- **โปรโตคอลเป็น big-endian ทั้งเส้นทาง** (handshake + คำสั่งทุกตัว) —
  แอป detect จาก handshake ให้เอง (server little-endian มาตรฐานก็ใช้ได้)
- **Sample rate ตั้งได้ครั้งเดียวต่อ connection** เฉพาะ {2.048M, 1.024M}
  — แอปส่งเป็นคำสั่งแรกเมื่อต่อใหม่ · **ห้ามขอ 512k** (server จะสตรีม
  1.024M มาเงียบ ๆ จนเสียงยืด — เคยเป็นบั๊กยาวบนเครื่องจริง)
- **stream-rate watchdog** วัด rate จริงจากไหลข้อมูล (6 วิ) แล้ว snap
  เฉพาะค่าที่ server รองรับจริง
- **ตั้ง gain ด้วย dB×10** (CMD 0x03 → CMD 0x04)
- ปลอดภัย: SetFrequency ส่งได้ตลอด · อย่าส่ง SetSampleRate กลางสตรีม
- มี dead-stream detector (ข้อมูลคงที่ ~3 วิ = ต่อใหม่) + reconnect/backoff
- dongle RTL-SDR Blog V4 (R828D) รับ 500 kHz – 1766 MHz; ต่ำกว่า 24 MHz
  สลับ HF direct sampling (Q branch) อัตโนมัติ ใช้ได้ทุกโหมดรวม SSB/CW

## พัฒนา/ทดสอบบนเครื่อง dev

```sh
go test ./...          # DSP + FT8 round-trip tests (สัญญาณสังเคราะห์)
go run . -demo -display png:shot.png -screenshot 4 -mode usb
```

## แผนต่อยอด (Roadmap)

- [x] FT8 decode (waterfall architecture + LDPC + CRC)
- [x] สเต็ปจูนตั้งได้ + span ถึง 3 kHz + ไม้บรรทัดความถี่
- [ ] OSD fallback สำหรับ FT8 สัญญาณอ่อนมาก (ระดับ -15 dB ลงไป)
- [ ] Stereo decoder สำหรับ WFM (pilot 19 kHz + DSB-SC L−R)
- [ ] หน้า preset/bookmark + scan หาช่องที่มีสัญญาณ
- [ ] โหมด AM (airband) · CTCSS decode/กรองสำหรับ NFM
- [ ] Spectrum 3D

---

# SDRg35xx (English)

An rtl_tcp-client SDR receiver with a built-in **FT8 decoder** for the
Anbernic RG35XX Plus (H700, linux/arm64). Written in pure Go, rendering
straight to `/dev/fb0`, reading gamepad input from evdev, and piping
audio to `aplay` — no X11, no GPU, no external dependencies.

## Highlights

- **Waterfall display** with zoomable span (1000 kHz down to 3 kHz) and
  a tick ruler — at least five ticks per side of centre, the 3rd and
  5th labelled with absolute frequency
- **Five receive modes**: NFM / WFM / USB / LSB / CW — SSB/CW via
  complex bandpass sideband selection, works on HF through the V4's
  direct-sampling mode (auto-switched below 24 MHz)
- **Built-in FT8 decoding**: full-message STFT waterfall with soft
  Costas-correlation candidate search, LDPC(174,91) belief propagation
  and CRC-14 — the same stack WSJT-X uses, ported from kgoba/ft8_lib.
  Decodes up to **10 simultaneous messages** per ~1 s scan; the
  history window keeps the last 100 messages (SELECT opens a full
  scrollable view, d-pad navigates)
- **16-row settings menu**: per-digit frequency editor, mode, gain
  (full V4 table), squelch, sample rate, per-mode bandwidth, HF direct
  sampling, FT8 toggle, AGC, on-screen host/IP editor, Thai/English
  language, span, **tune step** (10 Hz – 100 kHz), volume, PNG
  screenshots, update check
- **OTA self-update**: checks a version file over the network on
  launch, downloads, sha256-verifies, swaps its own binary and re-execs
- Remote diagnostics: the app periodically HTTP-POSTs its log to the
  download server
- Status bar with signal meter, squelch state and live CPU/MEM/SWAP

## Controls

| Button        | Action                                              |
|---------------|-----------------------------------------------------|
| ← / →         | tune −/+ step (menu-configurable), hold to repeat   |
| ↑ / ↓         | tune ± 10× step                                     |
| A             | cycle mode NFM → WFM → USB → LSB → CW               |
| SELECT        | large FT8 history window (↑↓ line, ←→ page)         |
| Y             | FT8 slot marker — red line + timestamp every 15 s   |
| X             | squelch 4→8→12→16→OFF                               |
| L1/R1, VOL±   | volume −/+ by 1%                                    |
| MENU (short)  | settings menu · hold MENU/START 3 s to quit         |

## Using FT8

Tune to an FT8 band (e.g. **21.074 MHz, USB**), enable **FT8 Decode**
in the menu. Sync search is automatic — decoded messages appear in the
bottom-left overlay, the status line, and the SELECT history window.
Press Y at a slot start if you want 15-second boundary markers on the
waterfall to eyeball slot timing.

## Build & install

```sh
./build-rg35xx.sh        # cross-compiles linux/arm64 into dist/rg35xx/
./upload.sh              # build + publish an OTA release (needs ftp.env)
```

Copy `SDRg35xx/`, `SDRg35xx.sh`, `SDRg35xx.png` into `Roms/APPS/` on the
SD card. Settings persist in `SDRg35xx/sdrg35xx.ini` (host, gain, step,
span, language, per-mode bandwidth, …).

## rtl_tcp server notes

The bundled default server is a custom rtl_tcp variant
(e25wop.thddns.net:2255, RTL-SDR Blog V4 / R828D):

- big-endian protocol throughout — auto-detected from the handshake
  (standard little-endian servers work too)
- sample rate can be set **once per connection**, {2.048M, 1.024M}
  only; asking for anything else silently yields a 1.024M stream —
  the app's rate watchdog snaps the DSP to the measured rate and only
  to genuinely supported values
- gain is set as dB×10 (tenths); SetFrequency is safe mid-stream
- dead-stream detection and automatic reconnect with backoff

## Development

```sh
go test ./...                          # DSP + FT8 synthetic round-trips
go run . -demo -display png:shot.png -screenshot 4 -mode usb
```

FT8 internals live in `internal/dsp/ft8*.go`: an incremental STFT
waterfall (93 symbol blocks, 2×2 time/frequency oversampling, a
custom 2560-point mixed-radix FFT — 5×512, verified against a direct
DFT), Costas-correlation candidate search, Gray-mapped soft LLR
extraction, the LDPC/CRC decoder and the WSJT-X message unpacker.

## Roadmap

- [x] FT8 decode (waterfall architecture, LDPC + CRC)
- [x] configurable tune step + 3 kHz span + frequency ruler
- [ ] OSD fallback decoding for very weak FT8 signals
- [ ] WFM stereo decoder · AM mode · CTCSS
- [ ] frequency presets / scanner
- [ ] 3D spectrum view
