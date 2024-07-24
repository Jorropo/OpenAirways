# RPC communication protocol between client hh-scope

This protocol is asynchronous and bidirectional. Communication is initiated by the server by sending an init packet.

The protocol is T\[L\]V, it always start with OpCode as `u16`.

The rest of the encoding is the concatenated values.

All numbers are little endian encoded.

All non whole byte values are rounded up to bytes sizes.

It is asynchronous the server will apply changes as soon as possible when it receives them, because they run on the same machine over pipes they should never get widely out of sync.

- `0x0000 <= OpCode < 0x0800` are game OpCodes which are sent from the client to the server.
- `0x0800 <= OpCode < 0xF000` are game OpCodes which are sent from the server to the client.

- `0x1000 <= OpCode < 0x1800` are meta client to server OpCodes. These do not control the game state.
- `0x1800 <= OpCode < 0x2000` are meta server to client OpCodes. These do not control the game state.

- `0x2000 <= OpCode < 0x3000` are meta local OpCodes. They are reserved however they are not sent over multiplayer. 

## OpCode table

The game OpCodes need to be impotent on each tick.

| OpCode | Name             | Arguments                                                                                                                                             | Size (without OpCode)                                                        |
|--------|------------------|-------------------------------------------------------------------------------------------------------------------------------------------------------|------------------------------------------------------------------------------|
| 0x0000 | DoNotUse         |                                                                                                                                                       | 0                                                                            |
| 0x0001 | GivePlaneHeading | `u32` plane id<br>`Rot16` new heading                                                                                                                 | 4 +<br>2                                                                     |
| 0x0800 | GameInit         | `u32` tickrate (hz)<br>`u5` SubPixel factor<br>`u32` plane speed<br>`u32x4` map size<br>`u32x4` camera size<br>`u8` runways (n)<br>- `Runway` entry   | 4 +<br>1 +<br>4 +<br>4 \* 4 +<br>4 \* 4 +<br>1 + (value of `n`)<br>`n` \* 11 |
| 0x0801 | StateUpdate      | `u32` current tick<br>`u32` planes (n)<br>- `Plane` entry                                                                                             | 4 +<br>4 + (value of `n`)<br>`n` \* 16                                       |
| 0x0802 | MapResize        | `u32x4` visible map                                                                                                                                   | 4 \* 4                                                                       |
| 0x2000 | CommitTick       |                                                                                                                                                       | 0                                                                            |

## Client to Server OpCode details

### 0x0000 - DoNotUse

Leave unused, the server crashes when sent this for debug purposes.

### 0x0001 - GivePlaneHeading

Give a new heading instruction to a plane, it will start turning in that direction and fly forward once the heading is reached.

## Server to Client OpCode details

### 0x0800 - GameInit

The first packet sent when connecting to the go server.

- `u32` tick rate in hz
- `u5` SubPixel factor, how many in game units make up a pixel (expressed as `1 << x`)
- `u32` speed of the plane in subpixels per tick
- `u32x4` size of the full map. (x, y, w, h). negative values for x and y are supported. (0,0) is typically expected to be the center.
- `u32x4` size of the camera. how much of the screen to show the user.
- `u8` `len(Runways)` the number of available runways; Then repeated for each runway:
  - `u8` id, unique runway id
  - `i32` x, center of runway, in subpixel units
  - `i32` y, center of runway in subpixel units
  - `Rot16` heading, rotation of the runway

### 0x0801 - StateUpdate

Continuous packets sent with the latest game state.

- `u32` now (current tick)
- `u32` `len(Planes)` the number of active planes; Then repeated for each plane:
  - `u32` id, unique plane id
  - `i32` x, in subpixel units
  - `i32` y, in subpixel units
  - `Rot16` wantHeading, heading the plane is turning towards
  - `Rot16` heading, current heading of the plane

### 0x0802 - MapResize
