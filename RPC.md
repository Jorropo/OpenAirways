# RPC between zig client and go server

The protocol is T\[L\]V, it always start with OpCode as `u16`.

The rest of the encoding is the concatenated values.

All numbers are little endian encoded.

All non whole byte values are rounded up to bytes sizes.

It is asynchronous the server will apply changes as soon as possible when it receives them, because they run on the same machine over pipes they should never get widely out of sync.

- `OpCode < 2**31` are game OpCodes and can be sent over multiplayer.
- `2 **15 <= OpCode < 2**15+2**14` are meta multiplayer, they are sent over multiplayer but do not control the game state.
- `2**15+2**14 <= OpCode` are meta local, they are not sent over multiplayer.

The game OpCodes need to be impotent on each tick.

| OpCode | Name             | Arguments                           | Size |
|--------|------------------|-------------------------------------|------|
| 0x0    | DoNotUse         |                                     | 0    |
| 0x1    | GivePlaneHeading | `u32` plane id; `Rot16` new heading | 6    |
| 0x8000 | CommitTick       |                                     | 0    |

### 0x0 - DoNotUse

Leave unused, the server crashes when sent this for debug purposes.

### 0x1 - GivePlaneHeading

Give a new heading instruction to a plane, it will start turning in that direction and fly forward once the heading is reached.

# RPC between go server and zig client

It starts with a game init packet which has this layout:
- `u32` tick rate in hz
- `u5` SubPixel factor, how many in game units make up a pixel (expressed as `1 << x`)
- `u32` speed of the plane in subpixel per tick

After this it continously send game state packets:

- `u32` now, current tick
- `u32` `len(Planes)` then repeated for each plane:
  - `u32` id, unique plane id
  - `i32` x, in subpixel units
  - `i32` y, in subpixel units
  - `Rot16` wantHeading, heading the plane is turning towards
  - `Rot16` heading, current heading of the plane