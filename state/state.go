package state

import (
	"math" // FIXME(@Jorropo): using float64 for Sin and Cos is likely not consistent based on hardware, if this cause desync replace with a pure uint16 implementation.
	"slices"
)

const (
	TickRate      = 20
	speed         = 40 / TickRate
	turnRate      = tau / 1 / TickRate            // Rot16 / 10s / tickRate gives turn rate per tick
	turnPerimeter = tau / turnRate * speed        // how long a complete 360° turn would be
	turnRadius    = turnPerimeter / (2 * math.Pi) // the length between the center of the turn circle and the plane
)

type Time uint32

// tau is one full turn as a Rot16
const tau = 1 << 16

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
	p                    XY
	WantHeading, heading Rot16
}

func (p *Plane) flyingStraight() bool {
	return p.WantHeading == p.heading
}

func (p *Plane) Position(now Time) (XY, Rot16) {
	if p.flyingStraight() {
		distance := float64((now - p.time) * speed)
		r := p.heading.Rad()
		return XY{p.p.X + int32(distance*math.Sin(r)), p.p.Y + int32(distance*math.Cos(r))}, p.heading
	}

	var toCenter Rot16
	diff := int16(p.WantHeading - p.heading)
	if diff < 0 {
		// left
		toCenter = p.heading - tau/4
	} else {
		// right
		toCenter = p.heading + tau/4
	}
	center_x := p.p.X + int32(turnRadius*math.Sin(toCenter.Rad()))
	center_y := p.p.Y + int32(turnRadius*math.Cos(toCenter.Rad()))
	arc := turnRate * Rot16(now-p.time)
	if diff < 0 {
		arc = -arc
	}
	toDest := -toCenter + arc
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
		p.p, _ = p.Position(now)
		p.heading = p.WantHeading
		p.time = now
	}
}

func (p *Plane) turn(now Time, heading Rot16) {
	p.p, p.heading = p.Position(now)
	p.WantHeading = heading
}

type State struct {
	planeId uint32
	Now     Time
	Planes  []Plane
}

func (s *State) Tick() {
	s.Now++

	// generating random traffic for testing purposes
	if s.Now%10 == 0 {
		s.Planes = append(s.Planes, Plane{
			ID:   s.planeId,
			time: s.Now,
		})
		s.planeId++
	}

	for i := range s.Planes {
		s.Planes[i].tick(s.Now)

		// randomly make them do loops for testing purpose
		if s.Planes[i].flyingStraight() {
			xy, heading := s.Planes[i].Position(s.Now)
			if abs(xy.Y) > 100 {
				s.Planes[i].turn(s.Now, -heading)
			}
		}
	}
}

func (s *State) Clone() *State {
	new := *s
	new.Planes = slices.Clone(new.Planes)
	return &new
}

func abs[T ~int | ~int8 | ~int16 | ~int32 | ~int64 | ~float32 | ~float64](a T) T {
	if a < 0 {
		a = -a
	}
	return a
}
