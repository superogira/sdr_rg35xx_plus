// aistest — live AIS feed check: connects to an NMEA-over-TCP server
// and reports decoded ships.
package main

import (
	"fmt"
	"os"
	"time"

	"sdr35/internal/ais"
)

func main() {
	host := "192.168.2.151:29420"
	if len(os.Args) > 1 {
		host = os.Args[1]
	}
	secs := 30
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &secs)
	}
	store := ais.NewStore()
	lines := 0
	client := ais.NewClient(host)
	done := make(chan struct{})
	go func() {
		client.Run(ctxStub{deadline: time.Now().Add(time.Duration(secs) * time.Second)}, store)
		close(done)
	}()
	for i := 0; i < secs/5; i++ {
		time.Sleep(5 * time.Second)
		fmt.Printf("t+%ds: %d ships\n", (i+1)*5, len(store.Ships()))
	}
	<-done
	fmt.Println("--- ships ---")
	for _, sh := range store.Ships() {
		fmt.Printf("%s  %-20s pos=%v sog=%.1f cog=%.0f", sh.MMSI, sh.Name, sh.HasPos, sh.SogKt, sh.CogDeg)
		if sh.HasPos {
			fmt.Printf("  %.5f %.5f", sh.Lat, sh.Lon)
		}
		fmt.Println()
	}
	_ = lines
}

type ctxStub struct{ deadline time.Time }

func (c ctxStub) Done() <-chan struct{} { return nil }
func (c ctxStub) Err() error {
	if time.Now().After(c.deadline) {
		return fmt.Errorf("done")
	}
	return nil
}
