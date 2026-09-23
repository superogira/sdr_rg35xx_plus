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
	"hint":       {"←→ จูน · SELECT โหมด · MENU เมนู (ค้าง 3 วิ = ออก)", "←→ tune · SELECT mode · MENU menu (hold 3s = exit)"},
	"menu_title": {"ตั้งค่า — SETTINGS", "SETTINGS"},
	"menu_hint":  {"A เลือก/ปรับ · B ปิด", "A select/adjust · B close"},
	"freq_title": {"ตั้งความถี่ (MHz)", "Set frequency (MHz)"},
	"freq_hint":  {"↑↓ เปลี่ยนตัวเลข · ←→ เลื่อนหลัก · A ยืนยัน · B ยกเลิก", "↑↓ digit · ←→ position · A OK · B cancel"},

	"m_freq":   {"ความถี่", "Frequency"},
	"m_mode":   {"โหมดรับ", "Mode"},
	"m_gain":   {"Gain", "Gain"},
	"m_sql":    {"Squelch", "Squelch"},
	"m_rate":   {"Sample Rate", "Sample Rate"},
	"m_bw":     {"Bandwidth", "Bandwidth"},
	"m_ds":     {"HF Direct Sampling", "HF Direct Sampling"},
	"m_agc":    {"AGC (SSB/CW)", "AGC (SSB/CW)"},
	"m_span":   {"Span จอ (zoom)", "Span (zoom)"},
	"m_step":   {"สเต็ปจูน", "Tune step"},
	"m_vol":    {"วอลุ่ม", "Volume"},
	"m_shot":   {"ถ่ายภาพหน้าจอ", "Screenshot"},
	"m_update": {"ตรวจอัพเดท", "Check update"},
	"m_lang":   {"ภาษา / Language", "Language / ภาษา"},
	"press_a":  {"กด A", "press A"},

	"ds_auto": {"อัตโนมัติ", "Auto"},
	"ds_on":   {"เปิด (Q branch)", "On (Q branch)"},
	"ds_off":  {"ปิด (tuner เสมอ)", "Off (tuner only)"},
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
