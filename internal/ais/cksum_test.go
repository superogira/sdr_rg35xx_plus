package ais

import (
	"fmt"
	"strings"
)

// decodeT fixes the dummy *00 checksum test builders emit, then decodes.
func (s *Store) decodeT(line string) {
	if x := strings.IndexByte(line, '*'); x >= 0 {
		body := strings.TrimLeft(line[:x], "!$")
		var sum byte
		for k := 0; k < len(body); k++ {
			sum ^= body[k]
		}
		line = fmt.Sprintf("%s*%02X", line[:x], sum)
	}
	s.Decode(line)
}
