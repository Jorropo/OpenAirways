package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strings"

	"github.com/Jorropo/OpenAirways/netcode"
	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	"github.com/multiformats/go-multiaddr"

	rpcgame "github.com/Jorropo/OpenAirways/rpc/game"
	"github.com/Jorropo/OpenAirways/state"
)

func main() {
	if err := mainRet(); err != nil {
		fmt.Fprintf(os.Stderr, "go server error: %v\n", err)
		os.Exit(1)
	}
}

func mainRet() error {
	var targetStr string
	var debugStartClients uint
	flag.StringVar(&targetStr, "target", "", "target multiaddr to connect to, leave empty for server")
	flag.UintVar(&debugStartClients, "debug-start-clients", 0, "start this many clients locally")
	flag.Parse()

	opts := []libp2p.Option{
		libp2p.Transport(tcp.NewTCPTransport), // only use TCP because we are using the linux process teardown to close the connection and QUIC runs in userland, could be changed.
	}

	var info peer.AddrInfo
	if targetStr == "" {
		log.Println("starting as server")
		opts = append(opts, libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0", "/ip6/::/tcp/0"))
	} else {
		// raw handling the string since it's 4am and I want to get this over with
		strs := strings.Split(targetStr, "/p2p/")
		if len(strs) != 2 {
			return fmt.Errorf("failing to parse target, too many p2p components")
		}

		var err error
		info.ID, err = peer.Decode(strs[1])
		if err != nil {
			return fmt.Errorf("parsing target's peerid: %w", err)
		}

		maddr, err := multiaddr.NewMultiaddr(strs[0])
		if err != nil {
			return fmt.Errorf("parsing target's maddr: %w", err)
		}
		info.Addrs = []multiaddr.Multiaddr{maddr}
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return fmt.Errorf("creating host: %w", err)
	}

	if debugStartClients > 0 {
		var laddr multiaddr.Multiaddr
		for _, a := range h.Addrs() {
			if strings.HasPrefix(a.String(), "/ip4/127.0.0.1/") {
				laddr = a
				goto startClients
			}
		}
		goto couldntStartClients
	startClients:
		{
			id := laddr.String() + "/p2p/" + h.ID().String()
			for range debugStartClients {
				cmd := exec.Command("./zig-out/bin/OpenAirways", "-target", id)
				cmd.Stdout = os.Stderr
				cmd.Stderr = os.Stderr
				err := cmd.Start()
				if err != nil {
					return fmt.Errorf("starting debug client: %w", err)
				}
			}
		}
	}
couldntStartClients:

	bus, err := h.EventBus().Subscribe(&event.EvtLocalAddressesUpdated{})
	if err != nil {
		return err
	}
	go func() {
		id := "/p2p/" + h.ID().String()
		for e := range bus.Out() {
			e := e.(event.EvtLocalAddressesUpdated)
			var a strings.Builder
			a.WriteString("Listening on:\n")
			for _, addr := range e.Current {
				switch addr.Action {
				case event.Added, event.Maintained:
				default:
					continue
				}
				a.WriteByte('\t')
				a.WriteString(addr.Address.String())
				a.WriteString(id)
				a.WriteByte('\n')
			}
			log.Print(a.String())
		}
	}()

	if targetStr != "" {
		err = h.Connect(context.TODO(), info)
		if err != nil {
			return fmt.Errorf("connecting to server: %w", err)
		}
	}

	n, err := netcode.New(h, makeRenderCallback(), info.ID)
	if err != nil {
		return fmt.Errorf("setting up netcode: %w", err)
	}

	var cmd rpcgame.Command
	for {
		_, err := io.ReadFull(os.Stdin, cmd[:2])
		if err != nil {
			return fmt.Errorf("reading header from zig: %v", err)
		}

		op := cmd.OpCode()
		sz, ok := op.Size()
		if !ok {
			return fmt.Errorf("unknown opcode from zig: %v", op)
		}

		_, err = io.ReadFull(os.Stdin, cmd[2:sz])
		if err != nil {
			return fmt.Errorf("reading payload from zig: %v", err)
		}

		n.Act(cmd)
	}
}

func makeRenderCallback() func(*state.State, func()) {
	var hasDoneInit bool
	var sendReuse []byte
	return func(s *state.State, unlock func()) {
		content := sendReuse[:0]
		if !hasDoneInit {
			hasDoneInit = true
			// Send game init packet
			const size = 2 + // OpCode
				4 + // TickRate
				1 + // SubPixel
				4 + // speed
				4*4 + // map size
				4*4 // visible map area
			content = makeBuffer(content, size)
			b := content
			b = u16(b, uint16(rpcgame.GameInit))
			b = u32(b, uint32(state.TickRate))
			b[0] = state.SubPixel
			b = b[1:]
			b = u32(b, uint32(state.Speed))
			b = rect(b, s.MapSize)
			b = rect(b, s.CameraSize)
		}

		size := 2 + // OpCode
			4 + // Now
			4 + // len(Planes)
			(4+ // id
				4*2+ // pos
				2+ // wantHeading
				2)* // heading
				uint(len(s.Planes))
		content, b := appendNewBufferAfter(content, size)

		b = u16(b, uint16(rpcgame.StateUpdate))
		b = u32(b, uint32(s.Now))
		b = u32(b, uint32(len(s.Planes)))
		for _, p := range s.Planes {
			b = u32(b, p.ID)
			pos, heading := p.Position(s.Now)
			b = v2(b, pos)
			b = u16(b, uint16(p.WantHeading))
			b = u16(b, uint16(heading))
		}
		unlock()

		sendReuse = content
		_, err := os.Stdout.Write(content)
		if err != nil {
			log.Fatalf("writing to zig client: %s", err)
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

func v2(b []byte, v state.V2) []byte {
	_ = b[:8]
	binary.LittleEndian.PutUint32(b, uint32(v.X))
	binary.LittleEndian.PutUint32(b[4:], uint32(v.Y))
	return b[8:]
}

func rect(b []byte, r state.Rect) []byte {
	_ = b[:16]
	binary.LittleEndian.PutUint32(b, uint32(r.X))
	binary.LittleEndian.PutUint32(b[4:], uint32(r.Y))
	binary.LittleEndian.PutUint32(b[8:], uint32(r.W))
	binary.LittleEndian.PutUint32(b[12:], uint32(r.H))
	return b[16:]
}

func makeBuffer(buf []byte, length uint) []byte {
	if uint(cap(buf)) < length {
		return append(buf[:cap(buf)], make([]byte, length-uint(cap(buf)))...)
	}
	return buf[:length]
}

func appendNewBufferAfter(b []byte, length uint) (total, new []byte) {
	total = append(b, make([]byte, length)...)
	new = total[len(b):]
	return
}
