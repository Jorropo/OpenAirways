# RPC between zig client and go server

The protocol is T\[L\]V, it always start with OpCode as `u32`.

The rest of the encoding is the concatenated values.

All numbers are little endian encoded.

It is asynchronous the server will apply changes as soon as possible when it receives them, because they run on the same machine over pipes they should never get widely out of sync.

| OpCode | Name             | Arguments                           | Size |
|--------|------------------|-------------------------------------|------|
| 0x0    | DoNotUse         |                                     | 0    |
| 0x1    | GivePlaneHeading | `u32` plane id; `Rot16` new heading | 6    |

### 0x0 - DoNotUse

Leave unused, the server crashes when sent this for debug purposes.

### 0x1 - GivePlaneHeading

Give a new heading instruction to a plane, it will start turning in that direction and fly forward once the heading is reached.