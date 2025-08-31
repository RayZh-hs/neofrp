package constant

import (
	"bytes"
	"io"
	"net"
	"time"
)

func NewBufferReadWriteCloser() *BufferReadWriteCloser {
	return &BufferReadWriteCloser{buf: &bytes.Buffer{}}
}

func (b *BufferReadWriteCloser) Read(p []byte) (int, error) {
	return b.buf.Read(p)
}

func (b *BufferReadWriteCloser) Write(p []byte) (int, error) {
	return b.buf.Write(p)
}

func (b *BufferReadWriteCloser) Close() error {
	return nil // Still no side effects
}

func (c *ChannelIdentifier) Increment() ChannelIdentifier {
	*c += 1
	return *c
}

func (bc *BufferedConn) Read(b []byte) (n int, err error) {
	return bc.Reader.Read(b)
}

func (bc *BufferedConn) Write(b []byte) (n int, err error) {
	return bc.Conn.Write(b)
}

func (bc *BufferedConn) Close() error {
	return bc.Conn.Close()
}

func (bc *BufferedConn) LocalAddr() net.Addr {
	return bc.Conn.LocalAddr()
}

func (bc *BufferedConn) RemoteAddr() net.Addr {
	return bc.Conn.RemoteAddr()
}

func (bc *BufferedConn) SetDeadline(t time.Time) error {
	return bc.Conn.SetDeadline(t)
}

func (bc *BufferedConn) SetReadDeadline(t time.Time) error {
	return bc.Conn.SetReadDeadline(t)
}

func (bc *BufferedConn) SetWriteDeadline(t time.Time) error {
	return bc.Conn.SetWriteDeadline(t)
}

func NewBidirectionalPipe() *BidirectionalPipe {
	reader, writer := io.Pipe()
	return &BidirectionalPipe{
		Reader: reader,
		Writer: writer,
	}
}

func NewBidirectionalPipePair() (*BidirectionalPipe, *BidirectionalPipe) {
	reader1, writer1 := io.Pipe()
	reader2, writer2 := io.Pipe()
	return &BidirectionalPipe{
			Reader: reader1,
			Writer: writer2,
		}, &BidirectionalPipe{
			Reader: reader2,
			Writer: writer1,
		}
}

func (pw *BidirectionalPipe) Read(b []byte) (n int, err error) {
	return pw.Reader.Read(b)
}

func (pw *BidirectionalPipe) Write(b []byte) (n int, err error) {
	return pw.Writer.Write(b)
}

func (pw *BidirectionalPipe) Close() error {
	err1 := pw.Reader.Close()
	err2 := pw.Writer.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

func IsQUICHandshake(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	return data[0]&0xC0 == 0xC0
}
