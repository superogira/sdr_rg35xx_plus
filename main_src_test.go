// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"
	"time"

	"sdr35/internal/gps"
)

// FIX source must produce a synthetic valid fix from the entered
// coordinates (and refuse 0,0), GPS source must require freshness.
func TestBeaconFixSource(t *testing.T) {
	f, ok := beaconFix("fix", nil, 13.5955, 100.56178)
	if !ok || f.Lat != 13.5955 || f.Lon != 100.56178 || !f.Valid {
		t.Fatalf("fix source = %+v ok=%v", f, ok)
	}
	if _, ok := beaconFix("fix", nil, 0, 0); ok {
		t.Fatal("0,0 must not beacon")
	}
	// GPS source with no receiver data: not valid.
	rx := gps.New()
	if _, ok := beaconFix("gps", rx, 1, 2); ok {
		t.Fatal("gps source must need a fresh fix")
	}
	_ = time.Now
}
