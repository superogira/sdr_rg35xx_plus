// Package i18n holds the two UI languages (Thai/English). Lang selects
// the active language ("th" default, "en"); T returns the string for a
// key. Kept as plain code so adding a language is adding a column.
package i18n

import "sync"

var (
	mu   sync.Mutex
	lang = "th"
)

// Lang returns the active language code.
func Lang() string {
	mu.Lock()
	defer mu.Unlock()
	return lang
}

// SetLang switches the active language ("th"/"en").
func SetLang(l string) {
	if l != "th" && l != "en" {
		return
	}
	mu.Lock()
	lang = l
	mu.Unlock()
}

var strings = map[string][2]string{
	// index 0 = th, 1 = en
	"hint":          {"←→ จูน · SELECT ประวัติ FT8 · MENU เมนู (ค้าง 3 วิ = ออก)", "←→ tune · SELECT FT8 log · MENU menu (hold 3s = exit)"},
	"menu_title":    {"ตั้งค่า — SETTINGS", "SETTINGS"},
	"menu_hint":     {"A เลือก/ปรับ · B ปิด", "A select/adjust · B close"},
	"menu_ver":      {"รุ่น %s · %s", "v%s · %s"},
	"freq_title":    {"ตั้งความถี่ (MHz)", "Set frequency (MHz)"},
	"freq_hint":     {"↑↓ เปลี่ยนตัวเลข · ←→ เลื่อนหลัก · A ยืนยัน · B ยกเลิก", "↑↓ digit · ←→ position · A OK · B cancel"},
	"kb_hint":       {"↑↓←→ เลื่อน · A พิมพ์ · B ลบ · L2 ตัวใหญ่ · X ยืนยัน", "↑↓←→ move · A type · B backspace · L2 shift · X OK"},
	"host_title":    {"รายการเครื่องแม่ข่าย", "Host list"},
	"host_hint":     {"A ใช้ · X แก้ไข · Y ลบ · B กลับ", "A use · X edit · Y delete · B back"},
	"host_add":      {"+ เพิ่มรายการใหม่", "+ Add new entry"},
	"beast_title":   {"รายการ Beast Server", "Beast server list"},
	"host_disable":  {"(ปิดใช้งาน — ไม่เชื่อมต่อ)", "(disabled — no connection)"},
	"host_usb":      {"USB — dongle ในเครื่อง", "USB — local dongle"},
	"m_clearcache":  {"ล้างแคชแผนที่", "Clear map cache"},
	"m_web":         {"เว็บควบคุม (LAN)", "Web control (LAN)"},
	"m_webport":     {"พอร์ตเว็บ", "Web port"},
	"m_lmute":       {"ปิดลำโพงเครื่อง", "Mute device speaker"},
	"m_aisrf":       {"รับ AIS จากคลื่น (RF)", "AIS RF decode"},
	"m_aislog":      {"ข้อความ AIS", "AIS messages"},
	"cache_cleared": {"ล้างแล้ว", "cleared"},
	"ais_title":     {"รายการ AIS Server", "AIS server list"},
	"ft8_scroll":    {"▲▼ บรรทัด · ◀▶ หน้า · B/Select ปิด", "▲▼ line · ◀▶ page · B/Select close"},

	"m_freq":             {"ความถี่", "Frequency"},
	"m_mode":             {"โหมดรับ", "Mode"},
	"m_gain":             {"Gain", "Gain"},
	"m_sql":              {"Squelch", "Squelch"},
	"m_rate":             {"Sample Rate", "Sample Rate"},
	"m_bw":               {"Bandwidth", "Bandwidth"},
	"m_ds":               {"HF Direct Sampling", "HF Direct Sampling"},
	"m_ppm":              {"แก้ความถี่ (ppm)", "Freq correction (ppm)"},
	"m_ft8":              {"ถอดรหัส FT8", "FT8 decode"},
	"m_host":             {"เครื่องแม่ข่าย / IP", "Host / IP"},
	"m_call":             {"คอลไซน์ของเรา", "My callsign"},
	"m_grid":             {"ลอเคเตอร์ (grid)", "Grid locator"},
	"m_psk":              {"ส่งรายงาน PSK Reporter", "PSK Reporter reporting"},
	"m_ant":              {"เสาอากาศ (PSK)", "Antenna (PSK)"},
	"m_rig":              {"เครื่องรับ (PSK)", "Rig (PSK)"},
	"m_wfmin":            {"Waterfall ระดับต่ำสุด", "Waterfall min level"},
	"m_wfmax":            {"Waterfall ระดับสูงสุด", "Waterfall max level"},
	"m_map":              {"แผนที่โลก FT8 >", "FT8 World Map >"},
	"m_adsbpage":         {"ADS-B / AIS", "ADS-B / AIS"},
	"m_gpspage":          {"GPS", "GPS"},
	"gps_dev":            {"อุปกรณ์", "Device"},
	"gps_stat":           {"สถานะ", "Status"},
	"gps_pos":            {"พิกัด", "Position"},
	"gps_grid":           {"Grid", "Grid"},
	"gps_alt":            {"ความสูง", "Altitude"},
	"gps_spd":            {"ความเร็ว", "Speed"},
	"gps_cse":            {"ทิศทาง", "Course"},
	"gps_sats":           {"ดาวเทียม", "Satellites"},
	"gps_hdop":           {"HDOP", "HDOP"},
	"gps_age":            {"ข้อมูลอายุ", "Data age"},
	"m_gpsfollow":        {"ใช้เป็นตำแหน่งรับ (radar)", "Use as receiver pos (radar)"},
	"gps_nofix":          {"ยังไม่มี fix", "no fix"},
	"gps_fix1":           {"Fix 3D/2D", "fix"},
	"gps_fix2":           {"Fix (DGPS)", "fix (DGPS)"},
	"gps_stale":          {"หยุดอัปเดต", "stale"},
	"gps_2d":             {"2D", "2D"},
	"gps_3d":             {"3D", "3D"},
	"gps_time":           {"เวลา (UTC)", "Time (UTC)"},
	"m_gpstimesync":      {"ตั้งนาฬิกาเครื่องจาก GPS", "Set clock from GPS"},
	"gps_clock_set":      {"ตั้งนาฬิกาจาก GPS (เดิมคลาด %v)", "clock set from GPS (was off by %v)"},
	"m_adsbhost":         {"Beast server", "Beast server"},
	"m_adsblat":          {"ละติจูดจุดรับ", "Receiver latitude"},
	"m_adsblon":          {"ลองจิจูดจุดรับ", "Receiver longitude"},
	"m_adsbradar":        {"จอเรดาร์ >", "Radar view >"},
	"radar_conn":         {"เชื่อมต่อแล้ว", "connected"},
	"hdr_planes":         {"บิน", "planes"},
	"hdr_ships":          {"เรือ", "ships"},
	"radar_noconn":       {"รอเชื่อมต่อ...", "waiting..."},
	"m_rtty":             {"RTTY ถอดรหัส", "RTTY decode"},
	"m_rttylog":          {"ข้อความ RTTY >", "RTTY text >"},
	"m_wefax":            {"WEFAX (แฟกซ์อากาศ) ถอดรหัส", "WEFAX (weather fax) decode"},
	"m_wefaxclear":       {"ล้างภาพ WEFAX", "Clear WEFAX image"},
	"m_wefaxauto":        {"WEFAX บันทึกอัตโนมัติ", "WEFAX auto save"},
	"m_cwdec":            {"CW ถอดรหัสอัตโนมัติ", "CW auto decode"},
	"m_cwclear":          {"ล้างข้อความ CW", "Clear CW text"},
	"wefax_hint":         {"Y บันทึก · ←→ จูน", "Y save · ←→ tune"},
	"wefax_idle":         {"รอ", "wait"},
	"wefax_phasing":      {"phasing", "phasing"},
	"wefax_rx":           {"รับ", "rx"},
	"wefax_saved":        {"บันทึก WEFAX: ", "WEFAX saved: "},
	"wefax_autosaved":    {"บันทึก WEFAX อัตโนมัติ: ", "WEFAX auto-saved: "},
	"wefax_empty":        {"WEFAX ยังไม่มีภาพ", "WEFAX: no image yet"},
	"wefax_cleared":      {"ล้างภาพ WEFAX แล้ว", "WEFAX image cleared"},
	"rtty_title":         {"ข้อความ RTTY (Baudot 45.45)", "RTTY text (Baudot 45.45)"},
	"rtty_hint":          {"Y กลับขั้ว · X ล้าง · B กลับ", "Y reverse · X clear · B back"},
	"m_bands":            {"ความถี่ FT8 >", "FT8 Frequencies >"},
	"m_audiopage":        {"เสียง", "Audio"},
	"m_aishost":          {"เซิร์ฟเวอร์ AIS (NMEA)", "AIS server (NMEA)"},
	"m_exit":             {"ออกจากแอป", "Exit app"},
	"sql_disabled":       {"ปิด SQL", "SQL off"},
	"sql_fmt":            {"SQL %.0fdBFS", "SQL %.0fdBFS"},
	"map_title":          {"แผนที่โลก FT8 - 10 นาที", "FT8 World Map - 10min"},
	"map_hint":           {"ซ้าย/ขวา เลือกสถานี · A ข้อมูล · B ปิด", "L/R station  A info  B close"},
	"map_style_hint":     {"L1/R1 แผนที่ · L2/R2 สี", "L1/R1 map style  L2/R2 colour"},
	"radar_hint":         {"L1/R1 ระยะ · L2/R2 แผนที่ · A/X/Y ป้าย/เป้า/ชื่อ · SEL เลือก · B ปิด", "L1/R1 range  L2/R2 map  A/X/Y labels/targets/name  SEL select  B close"},
	"radar_count":        {"%d เครื่องบิน", "%d aircraft"},
	"radar_count_ships":  {"  %d เรือ", "  %d ships"},
	"radar_count_hidden": {"  (+%d นอกระยะ)", "  (+%d beyond range)"},
	"d_mmsi":             {"MMSI", "MMSI"},
	"d_call":             {"Callsign", "Callsign"},
	"d_status":           {"สถานะ", "Status"},
	"d_sog":              {"SOG", "SOG"},
	"d_cog":              {"COG", "COG"},
	"d_hdg":              {"HDG", "HDG"},
	"d_pos":              {"พิกัด", "Position"},
	"d_type":             {"ชนิด", "Type"},
	"d_flag":             {"ธง", "Flag"},
	"d_dest":             {"ปลายทาง", "Destination"},
	"d_eta":              {"ETA", "ETA"},
	"d_dim":              {"ขนาด", "Size"},
	"d_draught":          {"กินน้ำ", "Draught"},
	"d_reg":              {"ทะเบียน", "Reg"},
	"d_icao":             {"ICAO", "ICAO"},
	"d_alt":              {"ความสูง", "Alt"},
	"d_spd":              {"ความเร็ว", "Speed"},
	"d_trk":              {"ทิศ", "Trk"},
	"d_dist":             {"ระยะ", "Dist"},
	"d_brg":              {"ทิศจากจุดรับ", "Bearing"},
	"d_vr":               {"ไต่/ลด", "V/S"},
	"d_last":             {"สัญญาณล่าสุด", "Last seen"},
	"d_selhint":          {"SELECT ปิดหน้าต่าง", "SELECT closes"},
	"m_af":               {"ตัวกรองเสียง", "Audio filter"},
	"af_narrow":          {"แคบ", "Narrow"},
	"af_normal":          {"ปกติ", "Normal"},
	"af_wide":            {"กว้าง", "Wide"},
	"af_custom":          {"กำหนดเอง", "Custom"},
	"m_nr":               {"ลดเสียงรบกวน (NR)", "Noise reduction (NR)"},
	"m_hp":               {"ตัดเสียงทุ้ม (HP)", "High-pass (HP)"},
	"m_lp":               {"ตัดเสียงแหลม (LP)", "Low-pass (LP)"},
	"band_title":         {"ความถี่ FT8", "FT8 frequencies"},
	"band_hint":          {"A จูน+เปิด FT8 · B กลับ", "A tune+FT8 on · B back"},
	"m_rxpage":           {"การรับ", "Receive"},
	"m_ft8page":          {"FT8 / รายงาน", "FT8 / Reporting"},
	"m_syspage":          {"ระบบ", "System"},
	"m_sysmon":           {"ข้อมูลเครื่อง >", "System monitor >"},
	"m_logs":             {"ดู log >", "View log >"},
	"m_bm":               {"รายการความถี่ >", "Bookmarks >"},
	"bm_add":             {"+ บันทึกความถี่ปัจจุบัน", "+ Save current"},
	"bm_hint":            {"A ไป · X แก้ชื่อ · Y ลบ · B กลับ", "A go · X rename · Y del · B back"},
	"sm_cpu_temp":        {"อุณหภูมิ CPU", "CPU temp"},
	"sm_gpu_temp":        {"อุณหภูมิ GPU", "GPU temp"},
	"sm_ve_temp":         {"อุณหภูมิ VE", "VE temp"},
	"sm_ddr_temp":        {"อุณหภูมิ DDR", "DDR temp"},
	"sm_batt_temp":       {"อุณหภูมิแบตเตอรี่", "Battery temp"},
	"sm_batt_lvl":        {"ระดับแบตเตอรี่", "Battery level"},
	"sm_batt_v":          {"แรงดันแบตเตอรี่", "Battery voltage"},
	"sm_batt_st":         {"สถานะแบตเตอรี่", "Battery status"},
	"sm_cpu_use":         {"CPU ใช้งาน", "CPU usage"},
	"sm_mem_use":         {"หน่วยความจำ", "Memory"},
	"sm_swap_use":        {"Swap", "Swap"},
	"m_agc":              {"AGC (SSB/CW)", "AGC (SSB/CW)"},
	"m_span":             {"Span จอ (zoom)", "Span (zoom)"},
	"span_fmt":           {"Span %d kHz", "Span %d kHz"},
	"m_step":             {"สเต็ปจูน", "Tune step"},
	"step_fmt":           {"สเต็ป %s", "Step %s"},
	"m_vol":              {"วอลุ่ม", "Volume"},
	"m_shot":             {"ถ่ายภาพหน้าจอ", "Screenshot"},
	"m_update":           {"ตรวจอัพเดท", "Check update"},
	"m_lang":             {"ภาษา / Language", "Language / ภาษา"},
	"press_a":            {"กด A", "press A"},

	"upd_only":       {"อัพเดทรองรับบนเครื่อง RG35XX เท่านั้น", "Update supported on the RG35XX only"},
	"upd_check_fail": {"เช็คอัพเดทไม่สำเร็จ: %v", "Update check failed: %v"},
	"upd_dl_fail":    {"โหลดไม่สำเร็จ: %v", "Download failed: %v"},
	"upd_http":       {"โหลดไม่สำเร็จ: HTTP %d", "Download failed: HTTP %d"},
	"upd_size":       {"ขนาดไฟล์ไม่ตรง (%d != %d)", "Package size mismatch (%d != %d)"},
	"upd_gzip":       {"ไฟล์เสีย (gzip): %v", "Corrupt package (gzip): %v"},
	"upd_nopath":     {"หาตำแหน่งโปรแกรมไม่ได้: %v", "Cannot locate the executable: %v"},
	"upd_write":      {"เขียนไฟล์ไม่ได้: %v", "Cannot write file: %v"},
	"upd_swap":       {"แทนที่ไฟล์ไม่ได้: %v", "Cannot replace file: %v"},
	"upd_restart":    {"รีสตาร์ทไม่สำเร็จ — ปิดแล้วเปิดใหม่", "Restart failed — please relaunch"},

	"ds_auto":     {"อัตโนมัติ", "Auto"},
	"ds_on":       {"เปิด (Q branch)", "On (Q branch)"},
	"ds_off":      {"ปิด (tuner เสมอ)", "Off (tuner only)"},
	"demo_host":   {"DEMO (สัญญาณจำลอง)", "DEMO (synthetic signal)"},
	"dead_stream": {"server ส่งข้อมูลเปล่า (ต้องรีสตาร์ท rtl_tcp server)", "server sends empty data (restart the rtl_tcp server)"},
	"on":          {"เปิด", "On"},
	"off":         {"ปิด", "Off"},

	"sql_off":       {"ปิด (Monitor)", "Off (Monitor)"},
	"listening":     {"กำลังฟัง ", "Listening "},
	"not_conn":      {"ไม่ได้เชื่อมต่อ ", "Not connected "},
	"connecting":    {"กำลังเชื่อมต่อ ", "Connecting "},
	"disconnected":  {"ขาดการเชื่อมต่อ: ", "Disconnected: "},
	"retry_in":      {" (ลองใหม่ใน %ds)", " (retry in %ds)"},
	"hold_exit":     {"กดค้างเพื่อออก… %.1fs", "Hold to exit… %.1fs"},
	"shot_ok":       {"บันทึกภาพแล้ว: ", "Screenshot saved: "},
	"shot_fail":     {"บันทึกภาพไม่สำเร็จ: ", "Screenshot failed: "},
	"uptodate":      {"เวอร์ชั่นล่าสุดแล้ว (%s)", "Up to date (%s)"},
	"downloading":   {"กำลังโหลดเวอร์ชั่นใหม่ %d …", "Downloading %d …"},
	"checksum":      {"checksum ไม่ตรง — ยกเลิก", "checksum mismatch — abort"},
	"updated":       {"อัพเดทเป็นเวอร์ชั่น %d แล้ว — กำลังรีสตาร์ท", "Updated to %d — restarting"},
	"ft8_synced":    {"FT8 sync แล้ว — รอสัญญาณถัดไป…", "FT8 synced — waiting for next signal…"},
	"ft8_need_sync": {"FT8: กด Y ตอนสัญญาณเริ่ม ก่อนใช้งาน", "FT8: press Y at slot start to sync"},
	"ft8_modelock":  {"FT8 เปิดอยู่ — โหมดล็อคเป็น USB", "FT8 is on — mode locked to USB"},
	"starting":      {"กำลังเริ่มระบบ…", "Starting…"},
}

// T returns the string for key in the active language.
func T(key string) string {
	pair, ok := strings[key]
	if !ok {
		return key
	}
	if Lang() == "en" {
		return pair[1]
	}
	return pair[0]
}
