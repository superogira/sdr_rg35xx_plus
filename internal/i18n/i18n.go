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
	"hint":       {"←→ จูน · SELECT ประวัติ FT8 · MENU เมนู (ค้าง 3 วิ = ออก)", "←→ tune · SELECT FT8 log · MENU menu (hold 3s = exit)"},
	"menu_title": {"ตั้งค่า — SETTINGS", "SETTINGS"},
	"menu_hint":  {"A เลือก/ปรับ · B ปิด", "A select/adjust · B close"},
	"menu_ver":   {"รุ่น %s · %s", "v%s · %s"},
	"freq_title": {"ตั้งความถี่ (MHz)", "Set frequency (MHz)"},
	"freq_hint":  {"↑↓ เปลี่ยนตัวเลข · ←→ เลื่อนหลัก · A ยืนยัน · B ยกเลิก", "↑↓ digit · ←→ position · A OK · B cancel"},
	"kb_hint":    {"↑↓←→ เลื่อน · A พิมพ์ · B ลบ · X ยืนยัน · Y ปิด", "↑↓←→ move · A type · B backspace · X OK · Y close"},
	"host_title": {"รายการเครื่องแม่ข่าย", "Host list"},
	"host_hint":  {"A ใช้ · X แก้ไข · Y ลบ · B กลับ", "A use · X edit · Y delete · B back"},
	"host_add":   {"+ เพิ่มรายการใหม่", "+ Add new entry"},
	"ft8_scroll": {"▲▼ บรรทัด · ◀▶ หน้า · B/Select ปิด", "▲▼ line · ◀▶ page · B/Select close"},

	"m_freq":   {"ความถี่", "Frequency"},
	"m_mode":   {"โหมดรับ", "Mode"},
	"m_gain":   {"Gain", "Gain"},
	"m_sql":    {"Squelch", "Squelch"},
	"m_rate":   {"Sample Rate", "Sample Rate"},
	"m_bw":     {"Bandwidth", "Bandwidth"},
	"m_ds":     {"HF Direct Sampling", "HF Direct Sampling"},
	"m_ft8":    {"ถอดรหัส FT8", "FT8 decode"},
	"m_host":   {"เครื่องแม่ข่าย / IP", "Host / IP"},
	"m_call":   {"คอลไซน์ของเรา", "My callsign"},
	"m_grid":   {"ลอเคเตอร์ (grid)", "Grid locator"},
	"m_psk":    {"ส่งรายงาน PSK Reporter", "PSK Reporter reporting"},
	"m_rxpage":  {"การรับ", "Receive"},
	"m_ft8page": {"FT8 / รายงาน", "FT8 / Reporting"},
	"m_syspage": {"ระบบ", "System"},
	"m_sysmon":  {"ข้อมูลเครื่อง ▸", "System monitor ▸"},
	"m_logs":    {"ดู log ▸", "View log ▸"},
	"sm_cpu_temp": {"อุณหภูมิ CPU", "CPU temp"},
	"sm_gpu_temp": {"อุณหภูมิ GPU", "GPU temp"},
	"sm_ve_temp":  {"อุณหภูมิ VE", "VE temp"},
	"sm_ddr_temp": {"อุณหภูมิ DDR", "DDR temp"},
	"sm_batt_temp": {"อุณหภูมิแบตเตอรี่", "Battery temp"},
	"sm_batt_lvl": {"ระดับแบตเตอรี่", "Battery level"},
	"sm_batt_v":   {"แรงดันแบตเตอรี่", "Battery voltage"},
	"sm_batt_st":  {"สถานะแบตเตอรี่", "Battery status"},
	"sm_cpu_use":  {"CPU ใช้งาน", "CPU usage"},
	"sm_mem_use":  {"หน่วยความจำ", "Memory"},
	"sm_swap_use": {"Swap", "Swap"},
	"m_agc":    {"AGC (SSB/CW)", "AGC (SSB/CW)"},
	"m_span":   {"Span จอ (zoom)", "Span (zoom)"},
	"m_step":   {"สเต็ปจูน", "Tune step"},
	"m_vol":    {"วอลุ่ม", "Volume"},
	"m_shot":   {"ถ่ายภาพหน้าจอ", "Screenshot"},
	"m_update": {"ตรวจอัพเดท", "Check update"},
	"m_lang":   {"ภาษา / Language", "Language / ภาษา"},
	"press_a":  {"กด A", "press A"},

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

	"ds_auto": {"อัตโนมัติ", "Auto"},
	"ds_on":   {"เปิด (Q branch)", "On (Q branch)"},
	"ds_off":     {"ปิด (tuner เสมอ)", "Off (tuner only)"},
	"demo_host":  {"DEMO (สัญญาณจำลอง)", "DEMO (synthetic signal)"},
	"dead_stream": {"server ส่งข้อมูลเปล่า (ต้องรีสตาร์ท rtl_tcp server)", "server sends empty data (restart the rtl_tcp server)"},
	"on":      {"เปิด", "On"},
	"off":     {"ปิด", "Off"},

	"sql_off":      {"ปิด (Monitor)", "Off (Monitor)"},
	"listening":    {"กำลังฟัง ", "Listening "},
	"not_conn":     {"ไม่ได้เชื่อมต่อ ", "Not connected "},
	"connecting":   {"กำลังเชื่อมต่อ ", "Connecting "},
	"disconnected": {"ขาดการเชื่อมต่อ: ", "Disconnected: "},
	"retry_in":     {" (ลองใหม่ใน %ds)", " (retry in %ds)"},
	"hold_exit":    {"กดค้างเพื่อออก… %.1fs", "Hold to exit… %.1fs"},
	"shot_ok":      {"บันทึกภาพแล้ว: ", "Screenshot saved: "},
	"shot_fail":    {"บันทึกภาพไม่สำเร็จ: ", "Screenshot failed: "},
	"uptodate":     {"เวอร์ชั่นล่าสุดแล้ว (%s)", "Up to date (%s)"},
	"downloading":  {"กำลังโหลดเวอร์ชั่นใหม่ %d …", "Downloading %d …"},
	"checksum":     {"checksum ไม่ตรง — ยกเลิก", "checksum mismatch — abort"},
	"updated":      {"อัพเดทเป็นเวอร์ชั่น %d แล้ว — กำลังรีสตาร์ท", "Updated to %d — restarting"},
	"ft8_synced":   {"FT8 sync แล้ว — รอสัญญาณถัดไป…", "FT8 synced — waiting for next signal…"},
	"ft8_need_sync": {"FT8: กด Y ตอนสัญญาณเริ่ม ก่อนใช้งาน", "FT8: press Y at slot start to sync"},
	"starting":     {"กำลังเริ่มระบบ…", "Starting…"},
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
