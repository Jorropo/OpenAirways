package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Jorropo/hh-scope/rollback"
	rpcgame "github.com/Jorropo/hh-scope/rpc/game"
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

	r := rollback.Rollback{
		Players: 1,
	}
	var lk sync.Mutex
	synchro := sync.Cond{L: &lk} // used with generation to know when to resend the state to the client

	go handleInbound(&lk, &synchro, &r)
	go func() {
		const delay = state.TickRate * 5 // for testing purposes commit with 5s of delay
		counter := delay
		for range ticks {
			lk.Lock()
			r.TickLive()
			if counter == 0 {
				if r.Live.Now%(8*state.TickRate) == 0 {
					var c rpcgame.Command
					b := u16(c[:], uint16(rpcgame.GivePlaneHeading))
					b = u32(b, 1) // id
					var heading state.Rot16
					if r.Live.Now%(16*state.TickRate) == 0 {
						heading = state.Tau / 4
					} else {
						heading = state.Tau / 4 * 3
					}
					b = u16(b, uint16(heading))
					r.Do(rollback.Command{Op: c, Reliable: true, HappendAt: r.Live.Now - delay/2}) // inject command in the past
					log.Println("injected turn 2.5s in the past")
				}
				r.DoCommit(r.Commit.Now)
			} else {
				counter--
			}
			synchro.Broadcast()
			lk.Unlock()
		}
	}()

	var lastSentState uint64
	var orig []byte
	{
		// Send game init packet
		const size = 4 + // TickRate
			1 + // SubPixel
			4 // speed
		orig = makeBuffer(orig, size)
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
		for r.LiveGen == lastSentState {
			synchro.Wait()
		}
		lastSentState = r.LiveGen

		size := 4 + // Now
			4 + // len(Planes)
			(4+ // id
				4+ // x
				4+ // y
				2+ // wantHeading
				2)* // heading
				uint(len(r.Live.Planes))
		orig := makeBuffer(orig, size)
		b := orig

		b = u32(b, uint32(r.Live.Now))
		b = u32(b, uint32(len(r.Live.Planes)))
		for _, p := range r.Live.Planes {
			b = u32(b, p.ID)
			xy, heading := p.Position(r.Live.Now)
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

func handleInbound(lk *sync.Mutex, synchro *sync.Cond, r *rollback.Rollback) {
	var b []byte
	for {
		b = makeBuffer(b, 2)
		_, err := io.ReadFull(os.Stdin, b)
		if err != nil {
			log.Fatalf("reading header: %v", err)
		}
		op := rpcgame.OpCode(binary.LittleEndian.Uint16(b))
		switch op {
		case rpcgame.GivePlaneHeading: // GivePlaneHeading
			const size = 2 + // OpCode
				4 + // id
				2 // heading
			b = makeBuffer(b, size)
			_, err = io.ReadFull(os.Stdin, b[2:])
			if err != nil {
				log.Fatalf("reading data: %v", err)
			}
			lk.Lock()
			genIdBefore := r.LiveGen
			r.Do(rollback.Command{Op: rpcgame.Command(b), Reliable: true, HappendAt: r.Live.Now})
			if genIdBefore != r.LiveGen {
				synchro.Broadcast()
			}
			lk.Unlock()
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

func makeBuffer(buf []byte, length uint) []byte {
	if uint(cap(buf)) < length {
		return append(buf[:cap(buf)], make([]byte, length-uint(cap(buf)))...)
	}
	return buf[:length]
}
