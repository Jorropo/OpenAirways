// TODO: add parsing and validation in this package
package rpcgame

import (
	"encoding/binary"
	"fmt"
	"math"
	"strconv"
)

const maximumSize = 8 // GivePlaneHeading

type Command [maximumSize]byte

// Bytes trim the bytes to the actual size of the command based on it's opcode.
// It panics if the command is invalid.
func (c *Command) Bytes() []byte {
	op := OpCode(binary.LittleEndian.Uint16(c[:]))
	sz, ok := op.Size()
	if !ok {
		panic(fmt.Sprint("invalid command opcode:", op))
	}
	return c[:sz]
}

func (c *Command) OpCode() OpCode {
	return OpCode(binary.LittleEndian.Uint16(c[:]))
}

type OpCode uint16

const (
	_ OpCode = iota
	GivePlaneHeading
)

func (o OpCode) String() string {
	switch o {
	case GivePlaneHeading:
		return "GivePlaneHeading"
	case CommitTick:
		return "CommitTick"
	default:
		return "OpCode(" + strconv.FormatUint(uint64(o), 10) + ")"
	}
}

// Size returns the size of the command in bytes including the opcode.
//
// FIXME: rework this when variably sized opcodes are added.
func (o OpCode) Size() (size uint, exists bool) {
	switch o {
	case GivePlaneHeading:
		return 8, true // opcode: u16, id: u32, heading: Rot16
	case CommitTick:
		return 2, true // opcode: u16
	default:
		return 0, false
	}
}

const (
	CommitTick OpCode = iota + 0x8000
)

func EncodeGivePlaneHeading(id uint32, heading Rot16) Command {
	var c Command
	binary.LittleEndian.PutUint16(c[:], uint16(GivePlaneHeading))
	binary.LittleEndian.PutUint32(c[2:], id)
	binary.LittleEndian.PutUint16(c[6:], uint16(heading))
	return c
}

// Tau is one full turn as a Rot16
const Tau = 1 << 16

type Rot16 uint16

func (r Rot16) Rad() float64 {
	return float64(r) / 65536 * math.Pi * 2
}

func EncodeCommitTick() Command {
	var c Command
	binary.LittleEndian.PutUint16(c[:], uint16(CommitTick))
	return c
}
