// TODO: add parsing and validation in this package
package rpcgame

const maximumSize = 8 // GivePlaneHeading

type Command [maximumSize]byte

type OpCode uint16

const (
	_ OpCode = iota
	GivePlaneHeading
)
