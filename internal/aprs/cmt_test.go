// SPDX-FileCopyrightText: 2026 superogira <SDRg35xx project>
// SPDX-License-Identifier: GPL-3.0-or-later

package aprs

import "testing"

func TestCommentBytes(t *testing.T) {
	for _, in := range []string{"=1333.00N/10030.00E- home qth", "!1337.20N/10035.40E>032/028 demo car /A=000040"} {
		p, ok := ParsePosition([]byte(in))
		if !ok {
			t.Fatal("parse failed")
		}
		t.Logf("%q -> comment %q (%d bytes) sym %c alt %d", in, p.Comment, len(p.Comment), p.Sym, p.AltFt)
	}
}
