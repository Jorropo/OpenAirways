package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/Jorropo/hh-scope/state"
)

func main() {
	if err := mainRet(); err != nil {
		fmt.Fprintf(os.Stderr, "go server error: %v\n", err)
		os.Exit(1)
	}
}

func mainRet() error {
	var tickMode string
	flag.StringVar(&tickMode, "debug-tickmode", "realtime", "tick mode is a debug flag that change the real world tick speed (realtime, slow (1hz), byte (one tick per byte on stdin))")
	flag.Parse()

	var ticks <-chan time.Time
	switch tickMode {
	case "realtime":
		ticks = time.Tick(time.Second / state.TickRate)
	case "slow":
		ticks = time.Tick(time.Second)
	default:
		return fmt.Errorf("unknown tick mode %q", tickMode)
	}

	current := &state.State{}
	var lk sync.Mutex
	synchro := sync.Cond{L: &lk}

	go func() {
		for range ticks {
			clone := current.Clone() // no need for sync since we are the only writer
			clone.Tick()
			lk.Lock()
			current = clone // the sender is reading tho
			synchro.Broadcast()
			lk.Unlock()
		}
	}()

	var lastSentTick state.Time
	var orig []byte
	{
		// Send game init packet
		size := 4 + // TickRate
			1 // SubPixelFactor
		orig = append(orig[:0], make([]byte, size)...)
		b := orig
		b = u32(b, uint32(state.TickRate))
		b[0] = state.SubPixelFactor
		_, err := os.Stdout.Write(orig)
		if err != nil {
			return fmt.Errorf("writing: %w", err)
		}
	}
	for {
		lk.Lock()
		for current.Now <= lastSentTick {
			synchro.Wait()
		}
		s := current
		lk.Unlock()
		lastSentTick = s.Now

		size := 4 + // Now
			4 + // len(Planes)
			(4+ // id
				4+ // x
				4+ // y
				2+ // wantHeading
				2)* // heading
				len(s.Planes)
		orig := append(orig[:0], make([]byte, size)...)
		b := orig

		b = u32(b, uint32(s.Now))
		b = u32(b, uint32(len(s.Planes)))
		for _, p := range s.Planes {
			b = u32(b, p.ID)
			xy, heading := p.Position(s.Now)
			b = u32(b, uint32(xy.X))
			b = u32(b, uint32(xy.Y))
			b = u16(b, uint16(p.WantHeading))
			b = u16(b, uint16(heading))
		}
		_, err := os.Stdout.Write(orig)
		if err != nil {
			return fmt.Errorf("writing: %w", err)
		}
	}
}

func u16(b []byte, x uint16) []byte {
	binary.LittleEndian.PutUint16(b, x)
	return b[2:]
}

func u32(b []byte, x uint32) []byte {
	binary.LittleEndian.PutUint32(b, x)
	return b[4:]
}
