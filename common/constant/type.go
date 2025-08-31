package constant

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"neofrp/common/multidialer"
	"net"
)

// BufferReadWriteCloser is a struct that implements the io.ReadWriteCloser interface
type BufferReadWriteCloser struct {
	buf *bytes.Buffer
}

type ChannelIdentifier uint16
type ClientAuthTokenType = string
type TransportType byte
type PortType uint16

// BufferedConn wraps a net.Conn with a bufio.Reader to handle cases where
// data has been read into the buffer during connection type detection
type BufferedConn struct {
	Reader *bufio.Reader
	Conn   net.Conn
}

// BidirectionalPipe wraps io.Pipe to implement the ReadWriteCloser interface
type BidirectionalPipe struct {
	Reader *io.PipeReader
	Writer *io.PipeWriter
}

type TaggedPort struct {
	PortType string
	Port     PortType
}

type SessionIndexCompound struct {
	Session *multidialer.Session
	Index   uint8
}

func (tp *TaggedPort) String() string {
	return tp.PortType + ":" + fmt.Sprint(tp.Port)
}

func (tp *TaggedPort) Bytes() []byte {
	// use big endian encoding
	switch tp.PortType {
	case "tcp":
		return []byte{PortTypeTCP, byte(tp.Port >> 8), byte(tp.Port & 0xFF)}
	case "udp":
		return []byte{PortTypeUDP, byte(tp.Port >> 8), byte(tp.Port & 0xFF)}
	default:
		return nil
	}
}

type ContextKeyType byte
