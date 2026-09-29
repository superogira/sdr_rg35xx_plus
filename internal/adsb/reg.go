package adsb

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// RegDB resolves ICAO hex → registration via hexdb.io, cached on disk.
// Unknown hexes are cached as "" so they are not asked again.
type RegDB struct {
	mu      sync.Mutex
	path    string
	regs    map[string]string
	pending map[string]bool
	client  *http.Client
}

func NewRegDB(path string) *RegDB {
	d := &RegDB{path: path, regs: map[string]string{}, pending: map[string]bool{},
		client: &http.Client{Timeout: 10 * time.Second}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if k, v, ok := strings.Cut(sc.Text(), "="); ok {
				d.regs[k] = v
			}
		}
		f.Close()
	}
	return d
}

// Lookup returns the cached registration, starting a background fetch
// on first sight. Returns "" while unknown.
func (d *RegDB) Lookup(icao string) string {
	icao = strings.ToUpper(icao)
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.regs[icao]; ok {
		return r
	}
	if !d.pending[icao] && len(d.pending) < 4 {
		d.pending[icao] = true
		go d.fetch(icao)
	}
	return ""
}

func (d *RegDB) fetch(icao string) {
	reg, ok := "", false
	resp, err := d.client.Get("https://hexdb.io/hex-reg?hex=" + icao)
	if err == nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if resp.StatusCode == 200 {
			ok = true
			if s := strings.TrimSpace(string(b)); s != "n/a" && !strings.ContainsAny(s, "<{ ") {
				reg = s
			}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.pending, icao)
	if !ok {
		return // network error: retry on a later sighting
	}
	d.regs[icao] = reg
	if f, err := os.OpenFile(d.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644); err == nil {
		fmt.Fprintf(f, "%s=%s\n", icao, reg)
		f.Close()
	}
}
