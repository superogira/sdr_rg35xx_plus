# SDRg35xx — RTL-SDR receiver for Anbernic RG35XX (Plus/H/2024)

แอปรับสัญญาณวิทยุ SDR บนเครื่องเกม Anbernic RG35XX Plus (H700 / linux arm64)
เขียนด้วย Go — รับ IQ จาก **RTL-SDR ผ่าน USB ของเครื่องโดยตรง** หรือจาก
**rtl_tcp server ทางเครือข่าย** แล้วถอดสัญญาณฟังเสียง + แสดงน้ำตก +
ถอดรหัสดิจิทัล พร้อมเว็บควบคุมผ่าน LAN

![SDRg35xx](dist/rg35xx/SDRg35xx.png)

## ความสามารถ

- โหมดรับ: WFM (วิทยุ FM), NFM, AM, USB, LSB, CW
- น้ำตก + สเปกตรัม บนจอเครื่อง (ซูม span ด้วย L1/R1) และบนเว็บ
  (แตะ/ลาก/ซูม 2 นิ้วได้)
- ถอดรหัส: **FT8** (+ ส่ง PSK Reporter, แผนที่โลก), **RTTY**, **CW**
  (ความเร็วอัตโนมัติ), **WEFAX/HF-FAX** (บันทึกรูป), **AIS** จากคลื่น RF
- **ADS-B radar** — รับจาก Beast server หรือ AIS/ADS-B TCP feed, แผนที่
  OSM หลายเลเยอร์, ธงประเทศ, breadcrumb
- เว็บควบคุม + ฟังเสียงผ่านเบราว์เซอร์ (ตั้งเปิดในเมนูระบบ)
- อัปเดตตัวเองผ่าน OTA

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

**ตั้งค่า → รายการเครื่องแม่ข่าย → "USB — dongle ในเครื่อง"**

แอปจะเรียก `rtl_tcp` ที่แนบมากับตัวแอปเอง (สร้างจาก
[rtlsdrblog/rtl-sdr-blog](https://github.com/rtlsdrblog/rtl-sdr-blog) —
รองรับ V4 + tuner รุ่นทั่วไป FC0013/E4000/R820T/FC0012/FC2580) ผูกกับ
`127.0.0.1:1234` แล้วเชื่อมต่อเองโดยอัตโนมัติ — ไม่ต้องติดตั้งอะไรเพิ่ม
(firmware ต้นฉบับมี libusb/libudev ที่ต้องใช้อยู่แล้ว) ตอนออกจากแอป
หรือสลับกลับไปใช้ TCP dongle จะถูกปล่อยให้โปรแกรมอื่นใช้ได้ทันที

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
รายการเครื่องแม่ข่ายของแอป เหมาะกับกรณีวางคอมไว้ข้างเสาอากาศ
หรือรับ HF ผ่าน direct sampling

## Build จากซอร์ส

```
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build .
```

หรือ `./build-rg35xx.sh` เพื่อสร้างแพ็กเกจพร้อม icon/launcher และ
`./upload.sh` เพื่อเผยแพร่ OTA (ตั้งค่า `ftp.env` ก่อน)

ไฟล์ `rtl_tcp` ใน `dist/rg35xx/` build จากซอร์ส rtl-sdr-blog แบบ
static librtlsdr (link แค่ libusb/libudev ของระบบ) — ดูขั้นตอนใน
คอมเมนต์ต้นไฟล์ `build-rg35xx.sh`

## License

See [Licenses](Licenses).
