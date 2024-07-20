package rollback

import (
	"bytes"
	"slices"

	rpcgame "github.com/Jorropo/hh-scope/rpc/game"
	"github.com/Jorropo/hh-scope/state"
)

type Command struct {
	Op        rpcgame.Command
	Reliable  bool // if reliable == false then we will discard it on commit
	HappendAt state.Time
}

type futureTicks struct {
	commited uint      // tracks how many players commited this tick
	cmds     []command // sorted by their rpcgame.Command representation
}

type command struct {
	Op       rpcgame.Command
	Reliable bool // if reliable == false then we will discard it on commit
}

type Rollback struct {
	Commit, Live state.State
	LiveGen      uint64 // because after a rollback live might change but have the same tickid we track modifications in LiveGen
	Players      uint

	// TODO: replace with a MaxOutOfTime sized ring buffer
	// index into []futureTicks + Commit.Now gives tick id to be applied on top of.
	join []futureTicks
}

// grabIdx computes the index into r.join and grows it if needed
func (r *Rollback) grabIdx(s state.Time) uint {
	idx := uint(s - r.Commit.Now)
	if idx >= l(r.join) {
		old := l(r.join)
		r.join = append(r.join, make([]futureTicks, idx+1-l(r.join))...)
		for ; old < l(r.join); old++ {
			r.join[old].commited = r.Players
		}
	}
	return idx
}

func (r *Rollback) Do(cmd ...Command) {
	var new bool
	canBeAppliedOnTopOfLive := true
	for _, c := range cmd {
		if c.HappendAt <= r.Commit.Now {
			panic("should be unreachable, netcode shouldn't let this through: giving commands before commit")
		}
		idx := r.grabIdx(c.HappendAt)
		ft := r.join[idx]
		i, ok := slices.BinarySearchFunc(ft.cmds, c.Op, func(a command, b rpcgame.Command) int {
			return bytes.Compare(a.Op[:], b[:])
		})
		if ok {
			ft.cmds[i].Reliable = ft.cmds[i].Reliable || c.Reliable
			continue // dups, don't reapply
		}
		canBeAppliedOnTopOfLive = canBeAppliedOnTopOfLive && i == len(ft.cmds) && c.HappendAt == r.Live.Now
		if canBeAppliedOnTopOfLive {
			r.Live.Apply(c.Op)
		}
		r.join[idx].cmds = slices.Insert(ft.cmds, i, command{c.Op, c.Reliable})
		new = true
	}
	if new {
		if canBeAppliedOnTopOfLive {
			return
		}
		// replay with the new commands
		tgt := r.Live.Now
		r.Live.Copy(&r.Commit)
		r.LiveGen++
		if l(r.join) > 0 {
			for _, c := range r.join[0].cmds {
				r.Live.Apply(c.Op)
			}
		}
		for tgt > r.Live.Now {
			r.Live.Tick()
			idx := uint(r.Live.Now - r.Commit.Now)
			if idx >= l(r.join) {
				continue
			}
			for _, c := range r.join[idx].cmds {
				r.Live.Apply(c.Op)
			}
		}
	}
}

func (r *Rollback) DoCommit(t state.Time) {
	idx := r.grabIdx(t)
	if r.join[idx].commited == 0 {
		panic("should be unreachable, netcode shouldn't let this through: trying to commit an already commited tick")
	}
	r.join[idx].commited--
	if r.join[idx].commited == 0 {
		if idx != 0 {
			panic("should be unreachable, netcode shouldn't let this through: trying to commit out of order")
		}
		for _, c := range r.join[0].cmds {
			r.Commit.Apply(c.Op)
		}
		r.Commit.Tick()
		r.join[0].cmds = nil // early gc
		r.join = r.join[1:]
	}
}

func (r *Rollback) TickLive() {
	r.Live.Tick()
	r.LiveGen++
	idx := uint(r.Live.Now - r.Commit.Now)
	if idx >= l(r.join) {
		return
	}
	for _, c := range r.join[idx].cmds {
		r.Live.Apply(c.Op)
	}
}

func l[S ~[]E, E any](s S) uint {
	return uint(len(s))
}
