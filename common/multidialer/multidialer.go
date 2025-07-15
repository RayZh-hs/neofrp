package multidialer

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/quic-go/quic-go"
)

const (
	// MaxPayloadSize defines the maximum size of the data part of a frame.
	// This is important for buffer allocation. 64KB is a common choice.
	MaxPayloadSize = 65535 // (2^16 - 1)
	// The header is 2 bytes for ID and 2 bytes for length.
	headerSize = 4
)

var (
	ErrSessionClosed = errors.New("session closed")
	ErrStreamClosed  = errors.New("stream closed")
)

// --- Interfaces ---

// Stream is an abstraction for a single bidirectional flow of data.
// It can be a QUIC stream or an emulated stream over TCP.
type Stream io.ReadWriteCloser

// Session manages multiple streams over a single underlying connection (TCP or QUIC).
type Session interface {
	// OpenStream creates a new bidirectional stream.
	OpenStream(ctx context.Context) (Stream, error)
	// AcceptStream waits for and returns the next stream created by the peer.
	AcceptStream(ctx context.Context) (Stream, error)
	// Close closes the session and all its streams with an error.
	Close(err error) error
	// RemoteAddr returns the remote address of the session.
	RemoteAddr() net.Addr
	// LocalAddr returns the local address of the session.
	LocalAddr() net.Addr
}

// --- Unified Constructors ---

// Dial connects to a remote address and returns a session.
// protocol must be "tcp" or "quic".
func Dial(ctx context.Context, protocol, address string, tlsConfig *tls.Config) (Session, error) {
	switch protocol {
	case "tcp":
		conn, err := tls.Dial("tcp", address, tlsConfig)
		if err != nil {
			return nil, err
		}
		return NewTCPSession(conn, true), nil // isClient = true
	case "quic":
		conn, err := quic.DialAddr(ctx, address, tlsConfig, nil)
		if err != nil {
			return nil, err
		}
		return NewQUICSession(conn), nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

type SessionListener interface {
	net.Listener
	AcceptSession() (Session, error)
}

// Listen starts a server and returns a listener that accepts sessions.
func Listen(ctx context.Context, protocol, address string, tlsConfig *tls.Config) (SessionListener, error) {
	switch protocol {
	case "tcp":
		l, err := tls.Listen("tcp", address, tlsConfig)
		if err != nil {
			return nil, err
		}
		return &tcpListener{Listener: l}, nil
	case "quic":
		l, err := quic.ListenAddr(address, tlsConfig, nil)
		if err != nil {
			return nil, err
		}
		return &quicListener{Listener: l}, nil
	default:
		return nil, fmt.Errorf("unsupported protocol: %s", protocol)
	}
}

// --- QUIC Implementation ---

// quicSession is a Session implementation that wraps a quic.Connection.
type quicSession struct {
	conn *quic.Conn
}

func NewQUICSession(conn *quic.Conn) Session {
	return &quicSession{
		conn: conn,
	}
}

func (s *quicSession) OpenStream(ctx context.Context) (Stream, error) {
	return s.conn.OpenStreamSync(ctx)
}

func (s *quicSession) AcceptStream(ctx context.Context) (Stream, error) {
	return s.conn.AcceptStream(ctx)
}

func (s *quicSession) Close(err error) error {
	// QUIC uses a numeric error code and a string.
	// We'll use a generic application error code.
	return s.conn.CloseWithError(0x100, err.Error())
}

func (s *quicSession) RemoteAddr() net.Addr {
	return s.conn.RemoteAddr()
}

func (s *quicSession) LocalAddr() net.Addr {
	return s.conn.LocalAddr()
}

// quicListener wraps a quic.Listener to accept Sessions.
type quicListener struct {
	*quic.Listener
}

func (l *quicListener) AcceptSession() (Session, error) {
	conn, err := l.Listener.Accept(context.Background())
	if err != nil {
		return nil, err
	}
	return NewQUICSession(conn), nil
}

// Modify the original Accept to use the new method for consistency
func (l *quicListener) Accept() (net.Conn, error) {
	session, err := l.AcceptSession()
	if err != nil {
		return nil, err
	}
	return &sessionConn{Session: session}, nil
}

// --- TCP Multiplexer Implementation ---

var bufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, MaxPayloadSize+headerSize)
		return &b
	},
}

// tcpSession emulates multiple streams over a single TCP connection.
type tcpSession struct {
	conn       io.ReadWriteCloser
	isClient   bool
	streams    sync.Map // map[uint16]*tcpStream
	acceptChan chan *tcpStream
	closeOnce  sync.Once
	closed     chan struct{}

	// Stream IDs are divided into odd/even to prevent collisions.
	// Client creates odd-numbered streams, server creates even-numbered streams.
	nextStreamID uint32
}

func NewTCPSession(conn io.ReadWriteCloser, isClient bool) Session {
	s := &tcpSession{
		conn:       conn,
		isClient:   isClient,
		acceptChan: make(chan *tcpStream, 128), // Buffer to avoid blocking the read loop
		closed:     make(chan struct{}),
	}
	// Initialize nextStreamID based on client/server role
	if isClient {
		s.nextStreamID = 1
	} else {
		s.nextStreamID = 2
	}

	go s.readLoop()
	return s
}

func (s *tcpSession) OpenStream(ctx context.Context) (Stream, error) {
	select {
	case <-s.closed:
		return nil, ErrSessionClosed
	default:
	}

	// Atomically get the next available ID for our role (odd/even)
	id := atomic.AddUint32(&s.nextStreamID, 2)
	stream := newTCPStream(uint16(id-2), s) // use the ID before incrementing
	s.streams.Store(stream.id, stream)

	return stream, nil
}

func (s *tcpSession) AcceptStream(ctx context.Context) (Stream, error) {
	select {
	case stream := <-s.acceptChan:
		return stream, nil
	case <-s.closed:
		return nil, ErrSessionClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *tcpSession) Close(err error) error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.conn.Close()

		// Terminate all active streams
		s.streams.Range(func(key, value interface{}) bool {
			if stream, ok := value.(*tcpStream); ok {
				stream.sessionClosed(err)
			}
			return true
		})
	})
	return nil
}

// For TCP, we don't have a net.Conn, so we need to fake it if needed.
// This implementation assumes the underlying conn is a net.Conn.
func (s *tcpSession) RemoteAddr() net.Addr {
	if c, ok := s.conn.(net.Conn); ok {
		return c.RemoteAddr()
	}
	return nil
}

func (s *tcpSession) LocalAddr() net.Addr {
	if c, ok := s.conn.(net.Conn); ok {
		return c.LocalAddr()
	}
	return nil
}

// readLoop is the heart of the TCP multiplexer. It reads frames from the
// underlying connection and dispatches them to the correct stream.
func (s *tcpSession) readLoop() {
	defer s.Close(io.EOF)

	header := make([]byte, headerSize)
	for {
		// Read the frame header
		_, err := io.ReadFull(s.conn, header)
		if err != nil {
			if err != io.EOF && !errors.Is(err, net.ErrClosed) {
				log.Error("tcpSession: failed to read header", "error", err)
			}
			return
		}

		id := binary.BigEndian.Uint16(header[0:2])
		length := binary.BigEndian.Uint16(header[2:4])

		// Read the frame payload
		bufPtr := bufferPool.Get().(*[]byte)
		data := (*bufPtr)[:length]
		_, err = io.ReadFull(s.conn, data)
		if err != nil {
			log.Error("tcpSession: failed to read payload", "error", err)
			bufferPool.Put(bufPtr)
			return
		}

		// Find the stream
		val, ok := s.streams.Load(id)
		if !ok {
			// This is a new stream initiated by the peer
			stream := newTCPStream(id, s)
			s.streams.Store(id, stream)
			stream.pushData(data) // Push initial data
			bufferPool.Put(bufPtr)

			select {
			case s.acceptChan <- stream:
			case <-s.closed:
				return
			}
		} else {
			// This is data for an existing stream
			stream := val.(*tcpStream)
			stream.pushData(data)
			bufferPool.Put(bufPtr)
		}
	}
}

func (s *tcpSession) writeFrame(id uint16, data []byte) (int, error) {
	select {
	case <-s.closed:
		return 0, ErrSessionClosed
	default:
	}

	if len(data) > MaxPayloadSize {
		return 0, fmt.Errorf("payload size %d exceeds MaxPayloadSize %d", len(data), MaxPayloadSize)
	}

	header := make([]byte, headerSize)
	binary.BigEndian.PutUint16(header[0:2], id)
	binary.BigEndian.PutUint16(header[2:4], uint16(len(data)))

	// In a real-world high-performance scenario, you'd use vectored I/O (Writev)
	// or a buffer pool for the combined write to avoid allocation.
	// For simplicity, we concatenate here.
	frame := append(header, data...)
	n, err := s.conn.Write(frame)
	if err != nil {
		s.Close(err) // If write fails, the session is likely broken
	}
	return n, err
}

func (s *tcpSession) removeStream(id uint16) {
	s.streams.Delete(id)
}

// tcpStream is an emulated stream over a tcpSession.
type tcpStream struct {
	id       uint16
	session  *tcpSession
	readBuf  *bytes.Buffer
	readMu   sync.Mutex
	readCond *sync.Cond

	// State management
	closeOnce   sync.Once
	closed      chan struct{}
	writeClosed bool
	readErr     error // Error to return on subsequent reads after close
}

func newTCPStream(id uint16, session *tcpSession) *tcpStream {
	s := &tcpStream{
		id:      id,
		session: session,
		readBuf: new(bytes.Buffer),
		closed:  make(chan struct{}),
	}
	s.readCond = sync.NewCond(&s.readMu)
	return s
}

func (s *tcpStream) Read(p []byte) (n int, err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	for s.readBuf.Len() == 0 {
		// If buffer is empty, check if the stream has been closed
		if s.readErr != nil {
			return 0, s.readErr
		}
		// Wait for data to be pushed or for the stream to be closed
		s.readCond.Wait()
	}

	// At this point, either data is available or an error was set.
	if s.readBuf.Len() > 0 {
		return s.readBuf.Read(p)
	}
	return 0, s.readErr
}

func (s *tcpStream) Write(p []byte) (n int, err error) {
	if s.writeClosed {
		return 0, ErrStreamClosed
	}

	// We must chunk the writes into frames smaller than MaxPayloadSize
	written := 0
	for len(p) > 0 {
		chunkSize := len(p)
		if chunkSize > MaxPayloadSize {
			chunkSize = MaxPayloadSize
		}
		chunk := p[:chunkSize]

		_, err := s.session.writeFrame(s.id, chunk)
		if err != nil {
			return written, err
		}

		written += chunkSize
		p = p[chunkSize:]
	}

	return written, nil
}

func (s *tcpStream) Close() error {
	s.closeOnce.Do(func() {
		close(s.closed)
		s.writeClosed = true
		// Send a zero-length frame to signal EOF to the other side
		s.session.writeFrame(s.id, []byte{})
		s.session.removeStream(s.id)
		// Wake up any blocked Read calls
		s.readCond.Broadcast()
	})
	return nil
}

// pushData is called by the session's readLoop to add data to this stream's buffer.
func (s *tcpStream) pushData(data []byte) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	// A zero-length data frame is our signal for EOF
	if len(data) == 0 {
		s.readErr = io.EOF
	} else {
		s.readBuf.Write(data)
	}

	// Signal any waiting Read call
	s.readCond.Signal()
}

// sessionClosed is called by the session when the entire connection is torn down.
func (s *tcpStream) sessionClosed(err error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	if s.readErr == nil {
		s.readErr = err
	}
	s.writeClosed = true
	s.readCond.Broadcast() // Wake up any readers
}

// tcpListener wraps a net.Listener to accept Sessions instead of Conns.
type tcpListener struct {
	net.Listener
}

func (l *tcpListener) AcceptSession() (Session, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	// isClient = false for server-side
	return NewTCPSession(conn, false), nil
}

// Modify the original Accept to use the new method for consistency
func (l *tcpListener) Accept() (net.Conn, error) {
	session, err := l.AcceptSession()
	if err != nil {
		return nil, err
	}
	return &sessionConn{Session: session}, nil
}

// --- Adapter to make Session behave like net.Conn ---

// sessionConn is an adapter that allows a Session to be used where a net.Conn is expected,
// for example, in http.Serve. It does this by opening a new stream for each call to Read/Write/Close.
// This is a simplified model; a real-world use case might require a more complex mapping.
// For an FRP service, you will likely work with the Session and Stream interfaces directly.
type sessionConn struct {
	Session
	stream Stream
	once   sync.Once
}

func (sc *sessionConn) getStream() (Stream, error) {
	var err error
	sc.once.Do(func() {
		// For a server-side conn, we accept a stream. For client-side, we'd open one.
		// This logic depends on context. For simplicity, we'll assume Accept.
		sc.stream, err = sc.Session.AcceptStream(context.Background())
	})
	return sc.stream, err
}

func (sc *sessionConn) Read(b []byte) (n int, err error) {
	s, err := sc.getStream()
	if err != nil {
		return 0, err
	}
	return s.Read(b)
}

func (sc *sessionConn) Write(b []byte) (n int, err error) {
	s, err := sc.getStream()
	if err != nil {
		return 0, err
	}
	return s.Write(b)
}

func (sc *sessionConn) Close() error {
	return sc.Session.Close(errors.New("connection closed by local"))
}

// SetDeadline, SetReadDeadline, SetWriteDeadline would need to be implemented
// on the Stream interface and its concrete types if required.
func (sc *sessionConn) SetDeadline(t time.Time) error      { return nil }
func (sc *sessionConn) SetReadDeadline(t time.Time) error  { return nil }
func (sc *sessionConn) SetWriteDeadline(t time.Time) error { return nil }
