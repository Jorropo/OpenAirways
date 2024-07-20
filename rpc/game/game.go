// TODO: add parsing and validation in this package
package rpcgame

const maximumSize = 10 // GivePlaneHeading

type Command [maximumSize]byte

type OpCode uint32

const (
	_ OpCode = iota
	GivePlaneHeading
)
