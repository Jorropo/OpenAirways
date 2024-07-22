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
	// FIXME: netcode is tightly coupled with this struct, merge the two ?

	Commit, Live state.State
	LiveGen      uint64 // because after a rollback live might change but have the same tickid we track modifications in LiveGen

	// TODO: replace with a MaxOutOfTime sized ring buffer
	// index into []futureTicks + Commit.Now gives tick id to be applied on top of.
	join [][]command
}

// Joins iterate all the jointures (rollback buffer) between Commit and Live.
func (r *Rollback) Joins(yield func(Command) bool) {
	base := r.Commit.Now
	for i, ft := range r.join {
		for _, c := range ft {
			if !yield(Command{Op: c.Op, Reliable: c.Reliable, HappendAt: state.Time(i) + base}) {
				return
			}
		}
	}
}

// grabIdx computes the index into r.join and grows it if needed
func (r *Rollback) grabIdx(s state.Time) uint {
	idx := uint(s - r.Commit.Now)
	if idx >= l(r.join) {
		r.join = append(r.join, make([][]command, idx+1-l(r.join))...)
	}
	return idx
}

func (r *Rollback) Do(cmd ...Command) (liveIsNew bool) {
	canBeAppliedOnTopOfLive := true
	for _, c := range cmd {
		if c.HappendAt <= r.Commit.Now {
			panic("should be unreachable, netcode shouldn't let this through: giving commands before commit")
		}
		idx := r.grabIdx(c.HappendAt)
		ft := r.join[idx]
		i, ok := slices.BinarySearchFunc(ft, c.Op, func(a command, b rpcgame.Command) int {
			return bytes.Compare(a.Op[:], b[:])
		})
		if ok {
			ft[i].Reliable = ft[i].Reliable || c.Reliable
			continue // dups, don't reapply
		}
		canBeAppliedOnTopOfLive = canBeAppliedOnTopOfLive && i == len(ft) && c.HappendAt == r.Live.Now
		if canBeAppliedOnTopOfLive {
			r.Live.Apply(c.Op)
		}
		r.join[idx] = slices.Insert(ft, i, command{c.Op, c.Reliable})
		liveIsNew = true
	}
	if liveIsNew {
		if canBeAppliedOnTopOfLive {
			return
		}
		// replay with the new commands
		tgt := r.Live.Now
		r.Live.Copy(&r.Commit)
		r.LiveGen++
		if l(r.join) > 0 {
			for _, c := range r.join[0] {
				r.Live.Apply(c.Op)
			}
		}
		for tgt > r.Live.Now {
			r.Live.Tick()
			idx := uint(r.Live.Now - r.Commit.Now)
			if idx >= l(r.join) {
				continue
			}
			for _, c := range r.join[idx] {
				r.Live.Apply(c.Op)
			}
		}
	}
	return
}

func (r *Rollback) TickCommit() {
	r.check()
	defer r.check()

	idx := r.grabIdx(r.Commit.Now)
	if idx != 0 {
		panic("should be unreachable, netcode shouldn't let this through: trying to commit out of order")
	}
	for _, c := range r.join[0] {
		if !c.Reliable {
			continue // discard unreliable commands
		}
		r.Commit.Apply(c.Op)
	}
	r.Commit.Tick()
	r.join[0] = nil // early gc
	r.join = r.join[1:]
}

func (r *Rollback) TickLive() {
	r.check()
	defer r.check()

	r.Live.Tick()
	r.LiveGen++
	idx := uint(r.Live.Now - r.Commit.Now)
	if idx >= l(r.join) {
		return
	}
	for _, c := range r.join[idx] {
		r.Live.Apply(c.Op)
	}
}

// check does sanity checks for correctness
func (r *Rollback) check() {
	if r.Commit.Now >= r.Live.Now {
		panic("live is not in commit's future")
	}
}

func l[S ~[]E, E any](s S) uint {
	return uint(len(s))
}
