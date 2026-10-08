package radio

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"sdr35/internal/dsp"
)

// TestFT8TSParamChangeRestarts: changing depth/threads/band while the
// sidecar runs must restart it cleanly (stay alive, keep decoding
// capability) — a half-restart would leave the tap feeding a dead pipe.
// Needs a node runtime + the ft8ts dist files; skips otherwise.
func TestFT8TSParamChangeRestarts(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node runtime")
	}
	repo := repoRoot(t)
	for _, f := range []string{"ft8ts.mjs", "ft8ts-worker-node.mjs"} {
		if _, err := os.Stat(filepath.Join(repo, "third_party", "ft8ts", f)); err != nil {
			t.Skip("ft8ts dist missing")
		}
	}
	side, err := os.ReadFile(filepath.Join(repo, "tools", "ft8ts_sidecar.mjs"))
	if err != nil {
		t.Skip("sidecar missing")
	}
	dir := t.TempDir()
	// stage the bundle exactly as Available() expects
	cp := func(src, dst string) {
		b, err := os.ReadFile(src)
		if err != nil {
			t.Skip(err.Error())
		}
		os.WriteFile(dst, b, 0o755)
	}
	cp(nodePath, filepath.Join(dir, "ft8ts-node"))
	if exeExt() != "" {
		cp(nodePath, filepath.Join(dir, "ft8ts-node"+exeExt()))
	}
	cp(filepath.Join(repo, "third_party", "ft8ts", "ft8ts.mjs"), filepath.Join(dir, "ft8ts.mjs"))
	cp(filepath.Join(repo, "third_party", "ft8ts", "ft8ts-worker-node.mjs"), filepath.Join(dir, "ft8ts-worker-node.mjs"))
	os.WriteFile(filepath.Join(dir, "ft8ts_sidecar.mjs"), side, 0o755)
	os.WriteFile(filepath.Join(dir, "ft8ts.rev"), []byte("r3\n"), 0o644)

	r := NewDemo(dsp.ModeUSB, nil)
	r.SetFT8TSDir(dir)
	if !r.SetFT8TSEnabled(true) {
		t.Fatal("enable failed")
	}
	time.Sleep(500 * time.Millisecond)
	if !ft8tsAlive(r) {
		t.Fatal("sidecar not alive after enable")
	}
	for _, p := range [][4]int{{3, 1, 200, 3000}, {1, 2, 500, 2500}, {2, 1, 200, 3000}} {
		r.SetFT8TSParams(p[0], p[1], p[2], p[3])
		time.Sleep(700 * time.Millisecond)
		if !ft8tsAlive(r) {
			t.Fatalf("sidecar dead after param change %v", p)
		}
		d, th, lo, hi := r.FT8TSParams()
		if d != p[0] || th != p[1] || lo != p[2] || hi != p[3] {
			t.Fatalf("params = %d/%d/%d/%d, want %v", d, th, lo, hi, p)
		}
	}
	// invalid values must not restart or corrupt
	before := ft8tsAlive(r)
	r.SetFT8TSParams(9, 9, 1, 99999)
	time.Sleep(300 * time.Millisecond)
	if ft8tsAlive(r) != before {
		t.Fatal("invalid params changed the engine")
	}
	r.SetFT8TSEnabled(false)
	time.Sleep(300 * time.Millisecond)
	if ft8tsAlive(r) {
		t.Fatal("sidecar alive after disable")
	}
}

func ft8tsAlive(r *Radio) bool {
	r.mu.Lock()
	eng := r.f8tsEng
	r.mu.Unlock()
	return eng != nil && eng.Alive()
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..")
}

func exeExt() string {
	if os.PathSeparator == 92 { // backslash
		return ".exe"
	}
	return ""
}
