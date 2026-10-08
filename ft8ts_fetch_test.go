package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"sdr35/internal/ft8ts"
)

// TestDownloadFT8TSBundleExtracts: the lazy-fetch path must download the
// real bundle over HTTP and lay down exactly the files Available()
// expects (basename-only, executable). Skips when the bundle tarball is
// not built locally.
func TestDownloadFT8TSBundleExtracts(t *testing.T) {
	gz, err := os.ReadFile("dist/ft8ts-bundle-linux-arm64.tar.gz")
	if err != nil || len(gz) < 1<<20 {
		t.Skip("bundle not built locally")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", itoa(len(gz)))
		w.Write(gz)
	}))
	defer srv.Close()

	dir := t.TempDir()
	if err := fetchFT8TSBundle(srv.URL, dir); err != nil {
		t.Fatalf("fetch: %v (status %q)", err, sidecarStatus("ft8ts"))
	}
	if !ft8ts.Available(dir) {
		t.Fatalf("bundle not available after fetch (status %q)", sidecarStatus("ft8ts"))
	}
	for _, f := range []string{"ft8ts-node", "ft8ts.mjs", "ft8ts-worker-node.mjs", "ft8ts_sidecar.mjs"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Errorf("missing %s: %v", f, err)
		}
	}
	if st, err := os.Stat(filepath.Join(dir, "ft8ts-node")); err == nil && runtime.GOOS != "windows" {
		if st.Mode()&0o111 == 0 {
			t.Fatal("ft8ts-node not executable")
		}
	}
	// status must have been set during the download and left clean
	if v := sidecarStatus("ft8ts"); v != "" {
		t.Fatalf("status left dirty: %q", v)
	}
}

// TestDownloadFT8TSBundleHTTP404: a non-200 must surface as an error
// (and the caller's retry loop logs it).
func TestDownloadFT8TSBundleHTTP404(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if err := fetchTarBundle("ft8ts", srv.URL+"/x.tar.gz", t.TempDir(), ft8ts.Available); err == nil {
		t.Fatal("404 did not error")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
