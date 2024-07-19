package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"os"
	"slices"
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
	flag.StringVar(&tickMode, "debug-tickmode", "realtime", "tick mode is a debug flag that change the real world tick speed (realtime, slow (1hz))")
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

	var s state.State
	var generation uint64 // generation is incremented on state modifications
	var lk sync.Mutex
	synchro := sync.Cond{L: &lk} // used with generation to know when to resend the state to the client

	go handleInbound(&lk, &synchro, &s, &generation)
	go func() {
		for range ticks {
			lk.Lock()
			s.Tick()
			generation++
			synchro.Broadcast()
			lk.Unlock()
		}
	}()

	var lastSentState uint64
	var orig []byte
	{
		// Send game init packet
		size := 4 + // TickRate
			1 + // SubPixel
			4 // speed
		orig = append(orig[:0], make([]byte, size)...)
		b := orig
		b = u32(b, uint32(state.TickRate))
		b[0] = state.SubPixel
		b = b[1:]
		b = u32(b, uint32(state.Speed))
		_, err := os.Stdout.Write(orig)
		if err != nil {
			return fmt.Errorf("writing: %w", err)
		}
	}
	for {
		lk.Lock()
		for generation == lastSentState {
			synchro.Wait()
		}
		lastSentState = generation

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
		lk.Unlock()
		_, err := os.Stdout.Write(orig)
		if err != nil {
			return fmt.Errorf("writing: %w", err)
		}
	}
}

func handleInbound(lk *sync.Mutex, cond *sync.Cond, s *state.State, generation *uint64) {
	var b []byte
	for {
		header := append(b[:0], make([]byte, 4)...)
		_, err := os.Stdin.Read(header)
		if err != nil {
			log.Fatalf("reading header: %v", err)
		}
		op := binary.LittleEndian.Uint32(header)
		switch op {
		case GivePlaneHeading: // GivePlaneHeading
			size := 4 + // id
				2 // heading
			b = append(b[:0], make([]byte, size)...)
			_, err = os.Stdin.Read(b)
			if err != nil {
				log.Fatalf("reading data: %v", err)
			}
			id := binary.LittleEndian.Uint32(b)
			heading := state.Rot16(binary.LittleEndian.Uint16(b[4:]))
			log.Printf("got GivePlaneHeading for plane %d: %v", id, heading)
			lk.Lock()
			i, ok := slices.BinarySearchFunc(s.Planes, id, func(p state.Plane, id uint32) int {
				other := p.ID
				if other < id {
					return -1
				}
				if other == id {
					return 0
				}
				return 1
			})
			if ok {
				s.Planes[i].Turn(s.Now, heading)
				*generation++
				cond.Broadcast()
				lk.Unlock()
			} else {
				lk.Unlock()
				// probably the user giving orders to a plane that just landed or left the map
				log.Println("got GivePlaneHeading for missing plane:", id)
			}
		default:
			log.Fatalf("got invalid opcode: %v", op)
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

const (
	_ = iota
	GivePlaneHeading
)
