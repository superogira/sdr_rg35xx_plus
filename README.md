# SDRg35xx — RTL-SDR receiver for Anbernic RG35XX (Plus/H/2024)

แอปรับสัญญาณวิทยุ SDR บนเครื่องเกม Anbernic RG35XX Plus (H700 / linux arm64)
เขียนด้วย Go — รับ IQ จาก **RTL-SDR ผ่าน USB ของเครื่องโดยตรง** หรือจาก
**rtl_tcp server ทางเครือข่าย** แล้วถอดสัญญาณฟังเสียง + แสดงน้ำตก +
ถอดรหัสดิจิทัล พร้อมเว็บควบคุมผ่าน LAN

![SDRg35xx](dist/rg35xx/SDRg35xx.png)

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

See [Licenses](Licenses) — ข้อความ license ฉบับเต็มของงาน third-party
ทั้งหมดที่แอปนี้ใช้/พอร์ต/รันร่วม (ตารางข้างบน) รวมอยู่ในโฟลเดอร์นั้น
