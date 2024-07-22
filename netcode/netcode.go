// Very Cute code.
//
// This whole package lack buffer control and will hapily allocate infinte memory as it receives more data.
package netcode

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"

	"github.com/Jorropo/hh-scope/rollback"
	rpcgame "github.com/Jorropo/hh-scope/rpc/game"
	"github.com/Jorropo/hh-scope/state"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

const Proto protocol.ID = "/hhs/0.0"

// release must be called when the renderer is done with the state
// release must be called before blocking on anything else.
type renderer func(state *state.State, release func())

type Netcode struct {
	// FIXME: this might be contentious, many only operate concurrent reads
	// ALL operations done while holding [lk] must be block free.
	lk sync.Mutex

	// Note: I use libp2p because it's convinient, might change to something more custom if it become hard (let's say WASM) or we need lower level timing control.
	// I might change my mind and write a 100% custom UDP (& webrtc-unreliable) based proto instead of the optional unreliable shortcuts I have in mind.
	h      host.Host
	render renderer
	target peer.ID // if empty then we are the server

	stateCond              sync.Cond
	rollback               rollback.Rollback
	commitWaitingOnPlayers []playersBlockingCommits // TODO: ring buffer this
	playersBlockingCommits uint32

	sendCond             sync.Cond
	send                 []sent // TODO: ring buffer this
	sendGen              uint64 // holds the generation of the last packet before send[0], else they are relative based on index
	playersWaitingOnSend uint32
	totalPlayers         uint32 // monotonically increasing player ids
}

func (n *Netcode) lastSentGen() uint64 {
	return uint64(len(n.send)) + n.sendGen
}

type sent struct {
	fromPlayerId       uint32 // used by the server to not loopback packets as well as do not send reliable packets over the unreliable mesh
	stillBlockedOnSend uint32
	when               state.Time
	cmd                rpcgame.Command
}

func (s *sent) decrementStillBlockedOnSend() {
	if s.stillBlockedOnSend == 0 {
		panic("decrementing stillBlockedOnSend even tho it is already zero")
	}
	s.stillBlockedOnSend--

}

// New creates and run a new netcode instance.
// If target is zero we run as a server.
// render will not be called concurrently.
func New(h host.Host, render renderer, target peer.ID) (*Netcode, error) {
	n := &Netcode{
		h:            h,
		render:       render,
		target:       target,
		totalPlayers: 1, // playerId 0 is always us
	}
	n.stateCond.L = &n.lk
	n.sendCond.L = &n.lk

	if n.target == "" {
		h.SetStreamHandler(Proto, func(s network.Stream) {
			c := s.Conn()
			log.Println("new connection", c.RemotePeer(), c.RemoteMultiaddr())

			if err := n.handleStreamAsServer(s); err != nil {
				log.Println(c.RemotePeer(), "stream error:", err)
			}
		})
		n.rollback.Live.Tick() // start with live in the future, commit must trail in the past.
		go n.tickLoop(time.Now(), 0)
	} else {
		if err := n.startupClientStreams(); err != nil {
			return nil, fmt.Errorf("startupClientStreams: %w", err)
		}
	}
	go n.renderLoop()

	return n, nil
}

func (n *Netcode) startupClientStreams() error {
	s, err := n.h.NewStream(context.TODO(), n.target, Proto)
	if err != nil {
		return fmt.Errorf("NewStream: %w", err)
	}
	var good bool
	defer func() {
		if !good {
			s.Reset()
		}
	}()

	_, err = n.rollback.Commit.Read(s)
	if err != nil {
		return fmt.Errorf("Reading commit state: %w", err)
	}
	n.rollback.Live.Copy(&n.rollback.Commit)

	// Estimating latency.
	start := time.Now()
	var theirLive [4]byte
	_, err = s.Write(theirLive[:1])
	if err != nil {
		return fmt.Errorf("writing timing: %w", err)
	}
	_, err = io.ReadFull(s, theirLive[:])
	if err != nil {
		return fmt.Errorf("reading timing: %w", err)
	}
	live := state.Time(binary.LittleEndian.Uint32(theirLive[:]))
	oneWayLatency := time.Since(start) / 2
	start = start.Add(oneWayLatency) // catchup to their time
	for range live - n.rollback.Commit.Now {
		n.rollback.Live.Tick()
	}

	n.playersWaitingOnSend++ // the server waits our messages

	go n.tickLoop(start, live)
	go n.clientSendLoop(s, live)
	go n.clientRecvLoop(s)
	good = true
	return nil
}

func (n *Netcode) clientRecvLoop(s network.Stream) {
	if err := func() error {
		defer s.Reset()

		var buf rpcgame.Command
		for {
			// FIXME: s.SetReadDeadline
			_, err := io.ReadFull(s, buf[:4])
			if err != nil {
				return fmt.Errorf("reading opcode: %w", err)
			}
			when := state.Time(binary.LittleEndian.Uint32(buf[:]))
			_, err = io.ReadFull(s, buf[:2])
			if err != nil {
				return fmt.Errorf("reading opcode: %w", err)
			}
			op := rpcgame.OpCode(binary.LittleEndian.Uint16(buf[:]))
			sz, ok := op.Size()
			if !ok {
				return fmt.Errorf("sent us invalid opcode: %v", op)
			}
			if sz > 2 {
				_, err = io.ReadFull(s, buf[2:sz])
				if err != nil {
					return fmt.Errorf("reading payload %v: %w", op, err)
				}
			}

			// FIXME: we can optimize this, instead of handling packet by packet we can batch with bufio.Reader and r.Buffered(), would create less sync events after Head-Of-Line event.
			// FIXME: use RPC namespaces in to sanitize allowed RPC calls.

			n.lk.Lock()
			switch op {
			case rpcgame.CommitTick:
				if when != n.rollback.Commit.Now {
					panic(fmt.Sprintf("inconsistent commit tick state, expected %d; got %d", when, n.rollback.Commit.Now))
				}
				n.rollback.TickCommit()
			default:
				if n.rollback.Do(rollback.Command{Op: buf, Reliable: true, HappendAt: when}) {
					n.stateCond.Broadcast()
				}
			}
			n.lk.Unlock()
		}
	}(); err != nil {
		log.Println("error in receive loop from:", s.Conn().RemotePeer(), "err:", err)
		os.Exit(1)
	}
}

// clientSendLoop is the main loop for sending packets to the server.
// sendTime is used because unlike in the server to client protocol when the server sends timing before each packet,
// the client to server protocol use implicit packets based on ordering, CommitTick implies a bump.
func (n *Netcode) clientSendLoop(s network.Stream, sendTime state.Time) {
	if err := func() error {
		defer s.Reset()

		var reuse []byte
		var lastSentGen uint64 // always start at zero for client
		for {
			n.lk.Lock()
			for n.lastSentGen() == lastSentGen {
				n.sendCond.Wait()
			}
			p := reuse[:0]
			for i := lastSentGen - n.sendGen; i < uint64(len(n.send)); i++ {
				n.send[i].decrementStillBlockedOnSend()
				todo := n.send[i]

				switch todo.cmd.OpCode() {
				case rpcgame.CommitTick:
					sendTime++
				}
				if todo.when != sendTime {
					panic(fmt.Sprintf("trying to send packets out of order to the server: %v %#+v", sendTime, todo))
				}

				p = append(p, todo.cmd.Bytes()...)
			}
			n.cleanupSends()
			lastSentGen = n.lastSentGen()
			n.lk.Unlock()

			if len(p) == 0 {
				// don't bother trying to write if none of the packet were sendable.
				// For example, let's say this player sent a reliable packet and this was the only packet we got woken up for, we wont send them anything else.
			} else {
				// FIXME: s.SetWriteDeadline

				if _, err := s.Write(p); err != nil {
					return err
				}
			}
		}
	}(); err != nil {
		log.Println("error in receive loop from:", s.Conn().RemotePeer(), "err:", err)
		os.Exit(1)
	}
}

func (n *Netcode) handleStreamAsServer(s network.Stream) (err error) {
	defer s.Reset()

	n.lk.Lock()
	// First: send our commited state.
	// Note: we can't send Live because other players might rollback before it. So they need to maintain their own rollback buffer.
	firstPacket := n.rollback.Commit.AppendMarshalBinary(nil)

	// Second: save all the reliable packets for them to catch-up.
	var catchup []byte
	for c := range n.rollback.Joins {
		if !c.Reliable {
			continue // The other player would thought theses are reliable if we send them here.
		}
		catchup = binary.LittleEndian.AppendUint32(catchup, uint32(c.HappendAt))
		catchup = append(catchup, c.Op.Bytes()...)
	}

	// we will need to sync them future packets.
	n.playersWaitingOnSend++
	lastSentGen := n.lastSentGen()
	playerId := n.totalPlayers
	n.totalPlayers++

	remoteNow := n.rollback.Live.Now
	remoteNow++                                         // it will be allowed to send us inputs on the next tick.
	idx := n.grabIdxInCommitWaitingOnPlayers(remoteNow) // make sure all up to this point already exists
	n.playersBlockingCommits++
	for i := idx; i < uint(len(n.commitWaitingOnPlayers)); i++ {
		// block us on future ticks
		n.commitWaitingOnPlayers[i]++
	}
	n.lk.Unlock()
	var readWasStarted bool
	cleanupPlayerReadEdge := func() {
		// player disconnected, cleanup read edge
		n.lk.Lock()
		defer n.lk.Unlock()

		n.playersBlockingCommits--
		for i := n.calcIdxInCommitWaitingOnPlayers(remoteNow); i < uint(len(n.commitWaitingOnPlayers)); i++ {
			// stop blocking yet to be commited ticks
			n.commitWaitingOnPlayers[i].decrement()
		}
		if n.cleanupCommits() {
			n.sendCond.Broadcast()
		}
	}

	defer func() {
		if err == nil {
			// avoid deadlock in case of panic
			return
		}

		if !readWasStarted {
			cleanupPlayerReadEdge()
		}

		// player disconnected, cleanup write edge
		n.lk.Lock()
		defer n.lk.Unlock()

		n.playersWaitingOnSend--
		for i := lastSentGen - n.sendGen; i < uint64(len(n.send)); i++ {
			// stop blocking yet to be sent packets
			n.send[i].decrementStillBlockedOnSend()
		}
		n.cleanupSends()
	}()

	// FIXME: s.SetWriteDeadline

	if _, err := s.Write(firstPacket); err != nil {
		return err
	}

	p := firstPacket[:0]

	// Let the client figure out the timing
	p = append(p[:0], 0, 0) // at least two bytes to error if the client doesn't us precisely one byte.
	red, err := s.Read(p)
	if err != nil {
		return err
	}
	if red != 1 {
		return fmt.Errorf("expected 1 for timing purposes, got %d", red)
	}

	p = binary.LittleEndian.AppendUint32(p[:0], uint32(remoteNow))

	if _, err := s.Write(p); err != nil {
		return err
	}

	if len(catchup) > 0 {
		// Then let it catchup
		if _, err := s.Write(catchup); err != nil {
			return err
		}
	}

	readWasStarted = true

	// Now start the main loops.
	go func() {
		if err := func() (err error) {
			defer s.Reset()
			defer func() {
				if err != nil {
					// avoid deadlock in case of panic
					cleanupPlayerReadEdge()
				}
			}()

			var buf rpcgame.Command
			for {
				// FIXME: s.SetReadDeadline
				_, err := io.ReadFull(s, buf[:2])
				if err != nil {
					return fmt.Errorf("reading opcode: %w", err)
				}
				op := rpcgame.OpCode(binary.LittleEndian.Uint16(buf[:]))
				sz, ok := op.Size()
				if !ok {
					return fmt.Errorf("sent us invalid opcode: %v", op)
				}
				if sz > 2 {
					_, err = io.ReadFull(s, buf[2:sz])
					if err != nil {
						return fmt.Errorf("reading payload %v: %w", op, err)
					}
				}

				// FIXME: we can optimize this, instead of handling packet by packet we can batch with bufio.Reader and r.Buffered(), would create less sync events after Head-Of-Line event.
				// FIXME: use RPC namespaces in to sanitize allowed RPC calls.

				n.lk.Lock()
				switch op {
				case rpcgame.CommitTick:
					commitedTick := remoteNow
					remoteNow++

					idx := n.grabIdxInCommitWaitingOnPlayers(commitedTick)
					n.commitWaitingOnPlayers[idx].decrement()
					if n.cleanupCommits() {
						n.sendCond.Broadcast()
					}
				default:
					// FIXME: optimization, .Do could tell us if this command was dup along of telling us if live is new, if it's dupped (and the previous one is not unreliable) we don't need to send this.
					if n.rollback.Do(rollback.Command{Op: buf, Reliable: true, HappendAt: remoteNow}) {
						n.stateCond.Broadcast()
					}
					if n.pushSent(playerId, remoteNow, buf) {
						n.sendCond.Broadcast()
					}
				}
				n.lk.Unlock()
			}
		}(); err != nil {
			log.Println("error in receive loop from:", s.Conn().RemotePeer(), "err:", err)
		}
	}()

	var reuse []byte // not reusing first packet since it's very likely way bigger than we ever need again, maybe it's fine to do so.
	// Then listen for new packets to send and forward them.
	for {
		n.lk.Lock()
		for n.lastSentGen() == lastSentGen {
			n.sendCond.Wait()
		}
		p := reuse[:0]
		for i := lastSentGen - n.sendGen; i < uint64(len(n.send)); i++ {
			n.send[i].decrementStillBlockedOnSend()
			todo := n.send[i]
			if todo.fromPlayerId == playerId {
				continue // don't loop back their own packets
			}

			p = binary.LittleEndian.AppendUint32(p, uint32(todo.when))
			p = append(p, todo.cmd.Bytes()...)
		}
		n.cleanupSends()
		lastSentGen = n.lastSentGen()
		n.lk.Unlock()

		if len(p) == 0 {
			// don't bother trying to write if none of the packet were sendable.
			// For example, let's say this player sent a reliable packet and this was the only packet we got woken up for, we wont send them anything else.
		} else {
			// FIXME: s.SetWriteDeadline

			if _, err := s.Write(p); err != nil {
				return err
			}
		}
	}
}

// grabIdxInCommitWaitingOnPlayers returns the index in commitWaitingOnPlayers.
func (n *Netcode) calcIdxInCommitWaitingOnPlayers(s state.Time) uint {
	return uint(s-n.rollback.Commit.Now) - 1 // minus one since we can't block Commit.Now so n.commitWaitingOnPlayers[0] is for Commit.Now+1
}

// grabIdxInCommitWaitingOnPlayers returns the index in commitWaitingOnPlayers for the given time and make sure it exists.
func (n *Netcode) grabIdxInCommitWaitingOnPlayers(s state.Time) uint {
	idx := n.calcIdxInCommitWaitingOnPlayers(s)
	if idx >= uint(len(n.commitWaitingOnPlayers)) {
		old := n.commitWaitingOnPlayers
		n.commitWaitingOnPlayers = append(n.commitWaitingOnPlayers, make([]playersBlockingCommits, idx+1-uint(len(n.commitWaitingOnPlayers)))...)
		new := n.commitWaitingOnPlayers[len(old):]
		for i := range new {
			new[i] = playersBlockingCommits(n.playersBlockingCommits)
		}
	}
	return idx
}

// cleanupSends removes all the sent packets that are no longer blocked on send.
// Must be called holding [n.lk].
func (n *Netcode) cleanupSends() {
	var i int
	var s sent
	for i, s = range n.send {
		if s.stillBlockedOnSend != 0 {
			break
		}
	}
	n.send = n.send[i:]
	n.sendGen += uint64(i)
}

// cleanupCommits execute commits all ticks that are no longer blocked on players.
// Must be called holding [n.lk].
// if needsToBroadcastSend == true the caller must call n.sendCond.Broadcast afterwards.
func (n *Netcode) cleanupCommits() (needToBroadcastSend bool) {
	if n.target != "" {
		panic("cleanupCommits must only be called on the server")
	}

	if n.rollback.Commit.Now >= n.rollback.Live.Now {
		panic("wrong state n.rollback.Commit.Live > n.rollback.Live.Now")
	}

	var ticked uint
	var p playersBlockingCommits
	for _, p = range n.commitWaitingOnPlayers {
		if p != 0 ||
			n.rollback.Commit.Now+1 >= n.rollback.Live.Now { // other clients might be in the future compared to us. Wait for us.
			break
		}
		ticked++
		needToBroadcastSend = n.tickCommit() || needToBroadcastSend
	}
	n.commitWaitingOnPlayers = n.commitWaitingOnPlayers[ticked:]
	if n.playersBlockingCommits == 0 && len(n.commitWaitingOnPlayers) == 0 {
		for n.rollback.Commit.Now+1 < n.rollback.Live.Now { // other clients might be in the future compared to us. Wait for us.
			needToBroadcastSend = n.tickCommit() || needToBroadcastSend
		}
	}
	return
}

// tickCommit ticks commit and sync it with clients if needed.
// Must be called holding [n.lk].
// if needsToBroadcastSent == true the caller must call n.sendCond.Broadcast afterwards.
func (n *Netcode) tickCommit() (needsToBroadcastSent bool) {
	oldTick := n.rollback.Commit.Now
	n.rollback.TickCommit()
	return n.pushSent(0, oldTick, rpcgame.EncodeCommitTick()) // FIXME: should we change wire to not change tick id for meta stuff ?
}

// pushSent adds a sent packet to the send queue.
// Must be called holding [n.lk].
// if needsToBroadcastSend == true the caller must call n.sendCond.Broadcast afterwards.
func (n *Netcode) pushSent(fromPlayerId uint32, when state.Time, cmd rpcgame.Command) (needsToBroadcastSend bool) {
	if n.playersWaitingOnSend == 0 {
		return false
	}
	n.send = append(n.send, sent{fromPlayerId, n.playersWaitingOnSend, when, cmd})
	return true
}

func (n *Netcode) renderLoop() {
	var lastRendered uint64
	for {
		n.lk.Lock()
		for n.rollback.LiveGen == lastRendered {
			n.stateCond.Wait()
		}
		lastRendered = n.rollback.LiveGen
		n.render(&n.rollback.Live, n.lk.Unlock)
	}
}

// start needs to have a monotonic component.
// for the client we need to wait until sendAfter to confirm (before we catchup).
func (n *Netcode) tickLoop(start time.Time, sendAfter state.Time) {
	const waitPerTick = time.Second / state.TickRate
	for {
		// Use a custom ticker to make sure we never get many ticks out of sync.
		// If time.Sleep is so slow we missed let's say 2 ticks, then we tick twice.
		dt := time.Since(start)
		todo := dt / waitPerTick
		if todo == 0 {
			time.Sleep(waitPerTick - dt)
			continue // retry check timing once it should be big enough
		} else {
			start = start.Add(todo * waitPerTick) // jump forward
		}

		n.lk.Lock()
		for range todo {
			n.rollback.TickLive()
		}
		var needsToBroadcastSend bool
		if n.target == "" {
			// server
			needsToBroadcastSend = n.cleanupCommits() // if all other clients are in the future (or there are no clients), we can the one blocking commit.
		} else {
			// client
			if n.rollback.Live.Now >= sendAfter {
				needsToBroadcastSend = n.pushSent(0, n.rollback.Live.Now, rpcgame.EncodeCommitTick())
			}
		}
		if needsToBroadcastSend {
			n.sendCond.Broadcast()
		}
		n.stateCond.Broadcast()
		n.lk.Unlock()

	}
}

// Act is from our own POV, it's our player doing something.
// It will update live be timestamped and synced with other players.
func (n *Netcode) Act(cmd rpcgame.Command) {
	n.lk.Lock()
	defer n.lk.Unlock()

	now := n.rollback.Live.Now
	if liveIsNew := n.rollback.Do(rollback.Command{Op: cmd, Reliable: true, HappendAt: now}); liveIsNew {
		n.stateCond.Broadcast()
	}

	if n.pushSent(0, now, cmd) {
		n.sendCond.Broadcast()
	}
}

type playersBlockingCommits uint32

func (p *playersBlockingCommits) decrement() {
	if *p == 0 {
		panic("decrementing playersBlockingCommits even tho it is already zero")
	}
	*p--
}
