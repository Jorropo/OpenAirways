package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"math" // FIXME(@Jorropo): using float64 for Sin and Cos is likely not consistent based on hardware, if this cause desync replace with a pure uint16 implementation.
	"slices"

	rpcgame "github.com/Jorropo/OpenAirways/rpc/game"
)

const (
	SubPixel       = 5
	SubPixelFactor = 1 << SubPixel
	TickRate       = 60
	Speed          = 40 * SubPixelFactor / TickRate // in SubPixel/tick
	turnRate       = Tau / 10 / TickRate            // Rot16 / 10s / tickRate gives turn rate per tick
	turnPerimeter  = Tau / turnRate * Speed         // how long a complete 360° turn would be
	turnRadius     = turnPerimeter / (2 * math.Pi)  // the length between the center of the turn circle and the plane
)

type Time uint32

// FIXME: completely move theses away
const Tau = rpcgame.Tau

type Rot16 = rpcgame.Rot16

type V2 struct {
	X, Y int32
}

type Rect struct {
	X, Y, W, H int32
}

type Plane struct {
	ID                   uint32
	time                 Time // last time position was materialized
	pos                  V2
	WantHeading, heading Rot16
}

func (p *Plane) flyingStraight() bool {
	return p.WantHeading == p.heading
}

func (p *Plane) Position(now Time) (V2, Rot16) {
	if p.flyingStraight() {
		distance := float64((now - p.time) * Speed)
		r := p.heading.Rad()
		return V2{p.pos.X + int32(distance*math.Sin(r)), p.pos.Y + int32(distance*math.Cos(r))}, p.heading
	}

	var toCenter Rot16
	diff := int16(p.WantHeading - p.heading)
	if diff < 0 {
		// left
		toCenter = p.heading - Tau/4
	} else {
		// right
		toCenter = p.heading + Tau/4
	}
	center_x := p.pos.X + int32(turnRadius*math.Sin(toCenter.Rad()))
	center_y := p.pos.Y + int32(turnRadius*math.Cos(toCenter.Rad()))
	arc := turnRate * Rot16(now-p.time)
	if diff < 0 {
		arc = -arc
	}
	toDest := toCenter + Tau/2 + arc
	xy := V2{
		center_x + int32(turnRadius*math.Sin(toDest.Rad())),
		center_y + int32(turnRadius*math.Cos(toDest.Rad())),
	}
	return xy, p.heading + arc
}

func (p *Plane) tick(now Time) {
	if p.flyingStraight() {
		return
	}

	dt := int(now - p.time)
	if dt*turnRate > abs(int(p.heading-p.WantHeading)) {
		p.pos, _ = p.Position(now)
		p.heading = p.WantHeading
		p.time = now
	}
}

func (p *Plane) Turn(now Time, heading Rot16) {
	p.pos, p.heading = p.Position(now)
	p.WantHeading = heading
	p.time = now
}

type State struct {
	nextPlaneId uint32 // monotonic increasing plane id
	Now         Time
	Planes      []Plane
	MapSize     Rect
	CameraSize  Rect
}

func (s *State) Tick() {
	s.Now++

	// generating some traffic for testing purposes
	if s.Now%(TickRate*5) == 1 && len(s.Planes) < 1 {
		s.Planes = append(s.Planes, Plane{
			ID:   s.nextPlaneId,
			time: s.Now,
		})
		s.nextPlaneId++
	}

	for i := range s.Planes {
		s.Planes[i].tick(s.Now)
	}
}

func (s *State) Apply(c rpcgame.Command) {
	b := c[:]
	op := rpcgame.OpCode(binary.LittleEndian.Uint16(b))
	b = b[2:]
	switch op {
	case rpcgame.GivePlaneHeading:
		id := binary.LittleEndian.Uint32(b)
		heading := Rot16(binary.LittleEndian.Uint16(b[4:]))
		i, ok := slices.BinarySearchFunc(s.Planes, id, func(p Plane, id uint32) int {
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
		} else {
			// probably the user giving orders to a plane that just landed or left the map
			log.Println("got GivePlaneHeading for missing plane:", id)
		}
	default:
		log.Fatalf("got invalid opcode: %v", op)
	}
}

// Copy copies o into s reusing s's storage
func (s *State) Copy(o *State) {
	*s = State{
		Now:         o.Now,
		nextPlaneId: o.nextPlaneId,
		Planes:      append(s.Planes[:0], o.Planes...),
	}
}

// Read reads the wire binary representation from r and writes to s.
func (s *State) Read(r io.Reader) (red uint, err error) {
	// FIXME: this is very trustfull and will panic or generate panics down the line if the input is malicious
	var b [max(headerSize, planeSize)]byte
	n, err := io.ReadFull(r, b[:headerSize])
	red += uint(n)
	if err != nil {
		return red, fmt.Errorf("reading When, planeId and len(Planes): %w", err)
	}
	s.Now = Time(binary.LittleEndian.Uint32(b[:]))
	s.nextPlaneId = binary.LittleEndian.Uint32(b[4:])
	nPlanes := binary.LittleEndian.Uint32(b[8:])

	s.Planes = slices.Grow(s.Planes[:0], int(nPlanes))
	for range nPlanes {
		n, err = io.ReadFull(r, b[:planeSize])
		red += uint(n)
		if err != nil {
			return red, fmt.Errorf("reading Plane: %w", err)
		}
		s.Planes = append(s.Planes, Plane{
			ID:          binary.LittleEndian.Uint32(b[:]),
			time:        Time(binary.LittleEndian.Uint32(b[4:])),
			pos:         V2{int32(binary.LittleEndian.Uint32(b[8:])), int32(binary.LittleEndian.Uint32(b[12:]))},
			WantHeading: Rot16(binary.LittleEndian.Uint16(b[16:])),
			heading:     Rot16(binary.LittleEndian.Uint16(b[18:])),
		})
	}
	return red, nil
}

func (s *State) UnmarshalBinary(b []byte) error {
	n, err := s.Read(bytes.NewReader(b))
	if err != nil {
		return err
	}
	if n != uint(len(b)) {
		return fmt.Errorf("extra trailing bytes in input: %d", n)
	}
	return nil
}

const headerSize = 4 + // Now
	4 + // nextPlaneId
	4 // len(Planes)

const planeSize = 4 + // id
	4 + // now (last materialized time)
	4 + // x
	4 + // y
	2 + // wantHeading
	2 // heading

// AppendMarshalBinary appends the wire binary representation of s to in and returns the result.
func (s *State) AppendMarshalBinary(in []byte) []byte {
	size := headerSize + planeSize*len(s.Planes)
	r := append(in, make([]byte, size)...)
	b := r[len(in):]

	b = u32(b, uint32(s.Now))
	b = u32(b, uint32(s.nextPlaneId))
	b = u32(b, uint32(len(s.Planes)))
	for _, p := range s.Planes {
		b = u32(b, p.ID)
		b = u32(b, uint32(p.time))
		b = u32(b, uint32(p.pos.X))
		b = u32(b, uint32(p.pos.Y))
		b = u16(b, uint16(p.WantHeading))
		b = u16(b, uint16(p.heading))
	}
	if len(b) != 0 {
		panic("State marshal logic error, didn't consumed all the buffer")
	}
	return r
}

func (s *State) MarshalBinary() ([]byte, error) {
	return s.AppendMarshalBinary(nil), nil
}

func abs[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64](a T) T {
	if a < 0 {
		a = -a
	}
	return a
}

func u16(b []byte, x uint16) []byte {
	binary.LittleEndian.PutUint16(b, x)
	return b[2:]
}

func u32(b []byte, x uint32) []byte {
	binary.LittleEndian.PutUint32(b, x)
	return b[4:]
}
