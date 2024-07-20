package state

import (
	"encoding/binary"
	"log"
	"math" // FIXME(@Jorropo): using float64 for Sin and Cos is likely not consistent based on hardware, if this cause desync replace with a pure uint16 implementation.
	"slices"

	rpcgame "github.com/Jorropo/hh-scope/rpc/game"
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

// Tau is one full turn as a Rot16
const Tau = 1 << 16

type Rot16 uint16

func (r Rot16) Rad() float64 {
	return float64(r) / 65536 * math.Pi * 2
}

type XY struct {
	X, Y int32
}

type Plane struct {
	ID                   uint32
	time                 Time // last time position was materialized
	pos                  XY
	WantHeading, heading Rot16
}

func (p *Plane) flyingStraight() bool {
	return p.WantHeading == p.heading
}

func (p *Plane) Position(now Time) (XY, Rot16) {
	if p.flyingStraight() {
		distance := float64((now - p.time) * Speed)
		r := p.heading.Rad()
		return XY{p.pos.X + int32(distance*math.Sin(r)), p.pos.Y + int32(distance*math.Cos(r))}, p.heading
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
	xy := XY{
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
	planeId uint32
	Now     Time
	Planes  []Plane
}

func (s *State) Tick() {
	s.Now++

	// generating some traffic for testing purposes
	if s.Now%(TickRate*5) == 1 && len(s.Planes) < 2 {
		s.Planes = append(s.Planes, Plane{
			ID:   s.planeId,
			time: s.Now,
		})
		s.planeId++
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
		Now:     o.Now,
		planeId: o.planeId,
		Planes:  append(s.Planes[:0], o.Planes...),
	}
}

func abs[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64](a T) T {
	if a < 0 {
		a = -a
	}
	return a
}
