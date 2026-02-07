package constant

import (
	"bufio"
	"bytes"
	"container/list"
	"fmt"
	"io"
	"neofrp/common/multidialer"
	"net"
	"sync"
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

// SessionList is a thread-safe wrapper around container/list.List.
// All operations on the underlying list must go through SessionList methods
// to avoid data races between port listeners and session management.
type SessionList struct {
	mu   sync.Mutex
	list *list.List
}

func NewSessionList() *SessionList {
	return &SessionList{list: list.New()}
}

func (sl *SessionList) PushBack(v interface{}) *list.Element {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.list.PushBack(v)
}

func (sl *SessionList) Remove(e *list.Element) interface{} {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.list.Remove(e)
}

func (sl *SessionList) Len() int {
	sl.mu.Lock()
	defer sl.mu.Unlock()
	return sl.list.Len()
}

// RoundRobin picks the next session using round-robin and returns the
// SessionIndexCompound. It holds the lock for the entire pick operation
// so that the list cannot be modified mid-iteration. nextIdx is updated
// in place to track position across calls.
func (sl *SessionList) RoundRobin(nextIdx *int) *SessionIndexCompound {
	sl.mu.Lock()
	defer sl.mu.Unlock()

	if sl.list.Len() == 0 {
		return nil
	}
	if *nextIdx >= sl.list.Len() {
		*nextIdx = 0
	}
	element := sl.list.Front()
	for i := 0; i < *nextIdx; i++ {
		if element == nil {
			*nextIdx = 0
			element = sl.list.Front()
			break
		}
		element = element.Next()
	}
	if element == nil {
		return nil
	}
	comp := element.Value.(*SessionIndexCompound)
	*nextIdx++
	return comp
}
