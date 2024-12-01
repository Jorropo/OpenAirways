# OpenAirways

Structure:
- `cmd/server` entry point for the server
- `game/main.zig` entry point, responsible for starting up the server

The server runs simulation and netcode, it comunicate with zig over STDIN / STDOUT async RPC.

All of the go code is based on an unblocked synchronisation primitive, it abuse unbounded buffers, all critical sections run without doing any IO or blocking operation of anykind.
For synchronisation it uses generation counters and `sync.Cond`.

Zig is more classical, it use two thread, one for rendering one for input. It synchronously writes to go.

Zig tries to accurately time frames, to do so it measure the second tick sent by go and linearly extrapolate plane positions backward and forward based on their speed.

Zig's render pipeline is thus not synced with go's ticks, both of it's threads do nonblocking syncs when they want to write or read to the state.

The netcode is rollback based, everyone run two complete simulation, one `commit` one `live`. `live` is what is drawn to the screen (\*linear extrapolation is added) is optimistic, it is synced from the server and fast forwarded to half one way latency (this means everyone sees the same state at the same time give or take).
Any user input is first applied to `live`, it is also timestamped and sent to the server.

The server is responsible for collecting and rebroadcasting all the timestampted inputs.

When someone (client and server) receives a packet from someone else they are almost always timestamped in the past thus it will insert it in the `join` buffer (/ rollback buffer).
This buffer is sorted by in-game timestamps, when some action is inserted in the middle it is in the "past" relative to `live`.

At this point we throw away `live`'s state, we copy `commit`'s state onto `live`, then we fast forward `live` to it's previous in-game time instantly, replaying all actions from the rollback buffer at their correct timestamp.

When a client passed a tick (it bumped it's own `live` counter) it send a `CommitTick` to the server, this allows the server to keep track of how far behind everyone is in the simulation (\*everyone should be at a very similar time, but due to ping and retransmissions the server's view of other players time varies and is behind), when it is certain that everyone passed some certain in-game time it ticks `commit` and broadcast `CommitTick` to all the players which will then also tick `Commit`.

We could in theory not use `commit` but because our rollback buffer doesn't keep track of the state rollbacks would become more and more expensive as we would have to replay the whole game over and over.
It also let us free memory.

Note: we don't rebroadcast self inputs, theses are added directly to the rollback buffer when timestamped by the client.

The main benefit is:
- easy perfect "client-side-prediction", everyone runs a complete simulation **with no delay**, so your own inputs instantly modify the world.
- compatible with high tickrates (the tick delay can be multiple orders of magnitude higher than the ping).
- a player dropping out or under high jitter does not impact the gameplay of other players.

Other benefits:
- easy to integrate with unreliable transport fast path, would reduce latency between players and remove head of line issues of the TCP commit backhaul.
- easy to make a replay system
  \*more exactly we already need to do all the hard parts which are:
  - repeatable deterministic simulation
  - serializing and ordering inputs

Draw backs:
- anything being stored in game state (inputs modifying it, entities moving, ...) needs to deterministically do their transformations
- when you watch an other players performing an action, significant teleportations can happen if they have high ping to you because you receive their packets in the past relative to your current play screen which is then fast-forwarded to you.
- exploitable by cheaters in some ways, they can purposely play in the past or future. I don't care tbh, that a co-op game, if someone cheat kick them out, or don't, you do you I don't care.

## How to run it

```
zig build run -- -debug-start-clients 1
```