# SDRg35xx — SDR receiver สำหรับ Anbernic RG35XX Plus

แอปรับฟังวิทยุผ่าน RTL-SDR แบบ rtl_tcp client ทำงานบนเครื่อง RG35XX Plus
(H700, linux/arm64) โดยเชื่อมต่อไปยัง rtl_tcp server ผ่าน Wi-Fi

- บนจอ: **waterfall** (±128 kHz รอบความถี่ที่จูน) พร้อมเส้นกลางชี้ความถี่
- แถบล่าง: ความถี่ตัวใหญ่, โหมด (NFM/WFM), มิเตอร์สัญญาณ dBFS, วอลุ่ม,
  gain/squelch และสถานะการเชื่อมต่อ (ข้อความไทย)
- โหมดรับ: **NFM** (วิทยุสื่อสาร 12.5/25 kHz, มี squelch อัตโนมัติ) และ
  **WFM** (วิทยุกระจายเสียง FM)
- เสียง: demod ในโปรเซสเดียว แล้วส่ง PCM ไป `aplay` (สำรอง `mpv`) —
  ตามวิธีที่พิสูจน์แล้วว่าใช้ได้บน StockOS นี้ (goro/net_radio ใช้แนวนี้)

## วิธี build

จากเครื่อง dev (Windows หรือ Linux):

```sh
./build-rg35xx.sh
```

## ปล่อยอัพเดทผ่านอินเทอร์เน็ต (OTA)

แอปเช็คเวอร์ชั่นจาก `https://downloads.catgg.net/sdrg35xx/version.txt`
ตอนเปิดโปรแกรม (และจากเมนู "ตรวจอัพเดท") — ถ้ามีเวอร์ชั่นใหม่กว่าจะโหลด
`sdrg35xx-linux-arm64.gz` ตรวจ sha256 แทนไฟล์ตัวเอง แล้วรีสตาร์ทเข้า
เวอร์ชั่นใหม่ทันที ไม่ต้องเสียบ SD card

ปล่อยเวอร์ชั่นใหม่ทำบนเครื่อง dev:

```sh
./upload.sh     # build → gzip → sha256 → FTP ขึ้น server
```

- รหัส FTP อยู่ใน `ftp.env` ข้างสคริปต์ (**ถูก .gitignore ไว้ ห้าม commit**)
  รูปแบบ: `FTP_HOST=…` / `FTP_USER=…` / `FTP_PASS=…` / `FTP_PATH=/`
- เลขเวอร์ชั่น = เวลา build (YYYYMMDDHHMM) ฝังใน binary อัตโนมัติ
- ปิด auto-check บนเครื่องเกมได้ที่ `sdrg35xx.ini`: `update=off`
  หรือเปลี่ยน server: `updateurl=https://…`

ได้แพ็กเกจใน `dist/rg35xx/`:

```
SDRg35xx/
  sdrg35xx     (linux/arm64, static, ไม่มี dependency)
SDRg35xx.sh       (launcher)
SDRg35xx.png      (icon)
```

## วิธีติดตั้งลงเครื่อง

1. คัดลอก `SDRg35xx/`, `SDRg35xx.sh`, `SDRg35xx.png` ไปที่ `Roms/APPS/` ของ SD card
2. เชื่อม Wi-Fi ให้เครื่องอยู่ในวงเดียวกับ rtl_tcp server
3. เปิดจากเมนู APPS (ถ้าไม่เห็น ให้รีสแกน/รีสตาร์ท)

log การรันอยู่ที่ `Roms/APPS/SDRg35xx-logfile.txt`
การตั้งค่า (ความถี่/โหมด/วอลุ่ม/gain/server) บันทึกอัตโนมัติที่
`Roms/APPS/SDRg35xx/sdrg35xx.ini` — แก้ได้ด้วยมือ เช่นเปลี่ยน `host=` หรือ `gain=`

## ปุ่มจอย

| ปุ่ม            | ทำงาน                                         |
|-----------------|-----------------------------------------------|
| ← / →           | จูน − / + ขั้น (NFM 12.5k, WFM 100k) กดค้าง = ทำซ้ำ |
| ↑ / ↓           | จูน ± 10× ขั้น                                 |
| A / SELECT      | สลับโหมด NFM ↔ WFM                            |
| X               | ปรับ squelch 4→8→12→16→OFF (OFF = ฟังตรง ๆ แบบ Monitor) |
| L1 / R1         | วอลุ่ม − / +                                  |
| VOL− / VOL+     | วอลุ่ม − / + ครั้งละ 2.5% (ล้อปรับเสียงข้างเครื่อง) |
| MENU (กดสั้น)   | เปิด/ปิดเมนูตั้งค่า                            |
| MENU ค้าง 3 วิ / START ค้าง 3 วิ | ออกจากแอป                 |

## เมนูตั้งค่า (กด MENU สั้น ๆ)

- **↑↓** เลื่อนแถว, **←→** ปรับค่า, **A** เลือก, **B** ปิดเมนู
- **โหมดรับ 5 โหมด**: NFM → WFM → **USB** → **LSB** → **CW** (วนด้วยปุ่ม
  A/SELECT หรือจากเมนู) — SSB/CW ใช้ complex bandpass เลือก sideband
  (USB: 200-2800 Hz, LSB: −2800..−200 Hz, CW: หน้าต่าง 300 Hz รอบเสียง
  beat 700 Hz) เหมาะกับ HF ผ่าน direct sampling
- **ความถี่**: กด A เข้าโหมดแก้เลขรายหลัก — `XXXX.XXXXX` MHz
  (↑↓ เปลี่ยนตัวเลข, ←→ เลื่อนหลัก, A ยืนยัน, B ยกเลิก)
- **โหมดรับ / Gain (ปรับสดตามตาราง V4) / Squelch / วอลุ่ม**: ปรับด้วย ←→
- **Sample Rate**: สลับ 2.048M ↔ 1.024M — server รับการตั้ง rate ได้
  **ครั้งเดียวต่อการเชื่อมต่อ** แอปจึง reconnect ให้เองตอนเปลี่ยน
  (สัญญาณจะหายไป 2-4 วิ)
- **Span จอ (zoom)**: ความกว้าง waterfall **1000 / 750 / 500 / 250 /
  125 / 100 / 50 kHz** — span แคบกว่าแบนด์ IF ดูจากข้อมูล IF ละเอียด
  (bin 500 Hz) ส่วน span กว้างกว่า สลับไปดูจากสัญญาณดิบเต็ม rate
  อัตโนมัติ (เหมือน zoom ของ SDR#) เก็บค่าใน `span=` ของ ini
- **ถ่ายภาพหน้าจอ**: กด A บันทึก PNG ลงโฟลเดอร์ SDRg35xx

## สถาปัตยกรรมและเหตุผล

- **Go แบบ pure (CGO_ENABLED=0)** คอมไพล์ข้ามเป็น linux/arm64 ได้จาก
  Windows เลย ตามแนวทางของ goro ที่รันบนเครื่องเดียวกัน
- **เขียน /dev/fb0 ตรง ๆ** ผ่าน `FBIOGET_VSCREENINFO` (ioctl) — เครื่องนี้ fb
  เป็น double buffer (virtual 640×960) ต้องเขียนที่ครึ่งที่กำลังแสดง
  (yoffset) ไม่งั้นภาพจะไปโผล่หน้าจอล่องหนแล้วจอค้างที่ Loading
  (เจอจริงรอบแรก) ค่า offset ของช่องสีอ่านจาก driver โดยตรง
- **อ่านปุ่มจาก evdev** (`/dev/input/event*`, หา device ชื่อ ANBERNIC)
  ตามแนวทาง net_radio
- **DSP ล้วน Go**: DC blocker → FIR decimate 2.048M→256k → FM demod
  (polar discriminator) → FIR decimate 256k→64k → de-emphasis (WFM) →
  soft-clip → resample 64k→48k → PCM s16 stereo ให้ aplay/mpv
- ทดสอบ DSP ด้วยสัญญาณสังเคราะห์: จูน tone 1 kHz ได้กลับมา 1002 Hz
  (WFM) และ 797 Hz (NFM), squelch เปิด/ปิด/hang ถูกพฤติกรรม

## ข้อควรรู้เกี่ยวกับ rtl_tcp server ตัวนี้ (e25wop.thddns.net:2255)

สิ่งที่พบจากการทดสอบจริง (ดู `cmd/testlisten`) — ยืนยันกับโค้ด
rtl-sdr-web-monitor ที่ใช้ server นี้อยู่แล้ว:

- **โปรโตคอลเป็น big-endian ทั้งเส้นทาง** (handshake และพารามิเตอร์ของ
  คำสั่งทุกตัว) — client ของแอป detect จาก handshake แล้วส่งให้ตรง
  อัตโนมัติ (server rtl_tcp มาตรฐานที่เป็น little-endian ก็ใช้ได้)
- **sample rate ตายตัวที่ 2.048 Msps** — คำสั่ง SetSampleRate ถูกเอาเฉย
  ทั้งแอปจึง hard-wire ที่ 2.048M และไม่ส่งคำสั่งนี้เลย
- **ตั้ง gain ด้วย dB**: ส่ง CMD 0x03 (manual) ตามด้วย CMD 0x04 โดย
  พารามิเตอร์ = dB×10 (สิบส่วน dB) — สูตรเดียวกับเวอร์ชันเว็บ
- คำสั่งที่ปลอดภัย: **SetFrequency** (ส่งได้ตลอด ใช้ตอนจูน) และ
  คำสั่ง gain (ส่งเฉพาะตอนต่อใหม่) — **อย่าส่ง SetSampleRate
  กลางสตรีม** (เคยทำให้สตรีมกลายเป็น 0x00 ทั้งหมด)
- แอปมี dead-stream detector (ข้อมูลคงที่ ~3 วิ = ตัดแล้วต่อใหม่เอง)
  และ reconnect อัตโนมัติพร้อม backoff
- dongle เป็น RTL-SDR Blog V4 (R828D, gain 0–49.6 dB); ช่วงรับ **500 kHz – 1766 MHz** — ต่ำกว่า 24 MHz สลับเข้าโหมด HF direct sampling (Q branch) อัตโนมัติ แล้วสลับกลับตัวจัดเมื่อกลับขึ้น VHF (FM/NFM เท่านั้น อ่าน SSB/CW ยังไม่รองรับ); default ของแอป
  ใช้ gain 40 dB แก้ได้ที่ `sdrg35xx.ini` (`gain=49.6` สูงสุด, `gain=-1`
  = AGC)

## พัฒนา/ทดสอบบนเครื่อง dev โดยไม่ต้องมีเครื่องเกม

```sh
go test ./...                       # DSP unit tests (สัญญาณสังเคราะห์)
go run . -demo -display png:shot.png -screenshot 4 -mode nfm
# → เรนเดอร์ UI+waterfall จากสัญญาณจำลอง 4 วิ ออกมาเป็น PNG

go run ./cmd/testlisten -scan -lo 143900000 -hi 146100000 -step 25000 -gain 28
# → สแกนย่าน 2M ผ่าน server จริง (FFT spectral contrast)

go run ./cmd/testlisten -freq 145100000 -mode nfm -seconds 10 -gain 24 -out out.wav
# → บันทึกเสียง demod จาก server จริงเป็น WAV พร้อมสถิติ
```

## แผนต่อยอด (Roadmap)

- [ ] Stereo decoder สำหรับ WFM (pilot 19 kHz + DSB-SC L−R)
- [ ] Spectrum 3D (perspective stack) แบบที่ rtl-sdr-web-monitor มี
- [ ] หน้า preset/bookmark ความถี่ + ฟีเจอร์ scan หาช่องที่มีสัญญาณ
- [ ] โหมด AM (airband 118–137 MHz)
- [ ] CTCSS decode/กรอง สำหรับ NFM
- [ ] ปรับ CPU: NEON ผ่าน assembly ถ้าจำเป็น (ตอนนี้ใช้แค่ ~20% หนึ่งคอร์)
