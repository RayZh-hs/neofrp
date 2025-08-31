package server

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	C "neofrp/common/constant"
	"neofrp/common/multidialer"
	"neofrp/common/safemap"

	"github.com/charmbracelet/log"
)

type TCPPortListener struct {
	TaggedPort  C.TaggedPort
	PortMap     *safemap.SafeMap[C.TaggedPort, *list.List]
	nextSession int
	mutex       sync.Mutex
}

func (l *TCPPortListener) Start() error {
	listener, err := net.Listen("tcp", net.JoinHostPort("0.0.0.0", fmt.Sprint(l.TaggedPort.Port)))
	if err != nil {
		return err
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				log.Warnf("Failed to accept connection for port %s: %v", l.TaggedPort.String(), err)
				continue
			}
			sessionList, ac := l.PortMap.Get(l.TaggedPort)
			l.mutex.Lock()
			if !ac || sessionList.Len() == 0 {
				l.mutex.Unlock()
				log.Warnf("No available session for port %s, closing connection", l.TaggedPort.String())
				conn.Close()
				continue
			}
			if l.nextSession >= sessionList.Len() {
				l.nextSession = 0
			}
			element := sessionList.Front()
			if element == nil {
				l.mutex.Unlock()
				log.Warnf("Session list is empty for port %s, closing connection", l.TaggedPort.String())
				conn.Close()
				continue
			}
			for i := 0; i < l.nextSession; i++ {
				if element == nil {
					log.Warnf("Session list modified during iteration for port %s, resetting nextSession", l.TaggedPort.String())
					l.nextSession = 0
					break
				}
				element = element.Next()
			}
			if element == nil {
				log.Warnf("No valid session found for port %s, closing connection", l.TaggedPort.String())
				l.mutex.Unlock()
				conn.Close()
				continue
			}
			comp := element.Value.(*C.SessionIndexCompound)
			l.nextSession++
			l.mutex.Unlock()

			log.Infof("Accepted connection for port %s from %s", l.TaggedPort.String(), conn.RemoteAddr())
			if comp.Session == nil {
				log.Warnf("Session is nil for port %s, closing connection", l.TaggedPort.String())
				conn.Close()
				continue
			}
			// Handle the connection with the session
			newConn, err := (*comp.Session).OpenStream(context.Background())
			if err != nil {
				log.Errorf("Failed to open stream for session %v: %v", comp.Session, err)
				conn.Close()
				continue
			}
			// First write in where to send the data (3 bytes header). Ensure full write.
			hdr := l.TaggedPort.Bytes()
			if len(hdr) != 3 {
				log.Errorf("Invalid tagged port bytes length: %d for %s", len(hdr), l.TaggedPort.String())
				newConn.Close()
				conn.Close()
				continue
			}
			written := 0
			// Instrumentation
			if ts, ok := newConn.(interface{ ID() uint16 }); ok {
				log.Debugf("server: wrote header for tcp stream id=%d port=%s", ts.ID(), l.TaggedPort.String())
			}
			for written < len(hdr) {
				n, werr := newConn.Write(hdr[written:])
				if werr != nil {
					log.Errorf("Failed to write tagged port to new connection: %v", werr)
					newConn.Close()
					conn.Close()
					continue
				}
				written += n
			}
			// Now use connection copy to relay data
			go func(localConn net.Conn, remoteConn multidialer.Stream) {
				defer localConn.Close()
				defer remoteConn.Close()

				// Create a context for this connection
				connCtx, cancel := context.WithCancel(context.Background())
				defer cancel()

				// Copy data bidirectionally
				go func() {
					defer cancel()
					io.Copy(remoteConn, localConn)
				}()

				go func() {
					defer cancel()
					io.Copy(localConn, remoteConn)
				}()

				// Wait for cancellation
				<-connCtx.Done()
			}(conn, newConn)
		}
	}()
	return nil
}

type UDPPortListener struct {
	TaggedPort C.TaggedPort
	PortMap    *safemap.SafeMap[C.TaggedPort, *list.List]
	// SourceMap maps remote UDP addr string (IP:port) -> multiplexed stream
	SourceMap   *safemap.SafeMap[string, *multidialer.Stream]
	nextSession int
	mutex       sync.Mutex
}

func (l *UDPPortListener) Start() error {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{
		IP:   net.ParseIP("0.0.0.0"),
		Port: int(l.TaggedPort.Port),
	})
	if err != nil {
		return fmt.Errorf("failed to listen on UDP port %s: %w", l.TaggedPort.String(), err)
	}
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				log.Warnf("Failed to read from UDP port %s: %v", l.TaggedPort.String(), err)
				continue
			}
			log.Infof("Received %d bytes from %s on UDP port %s", n, addr.String(), l.TaggedPort.String())
			// Handle the received data
			// First, check whether an existing stream exists for this address
			key := addr.String()
			stream, exists := l.SourceMap.Get(key)
			if exists {
				_, err = (*stream).Write(buf[:n])
				if err != nil {
					log.Errorf("Failed to write to existing stream for %s: %v", addr.String(), err)
					// Clean up the failed stream
					(*stream).Close()
					l.SourceMap.Delete(key)
					continue
				}
			} else {
				sessionList, ac := l.PortMap.Get(l.TaggedPort)
				if !ac || sessionList.Len() == 0 {
					log.Warnf("No available session for port %s, ignoring data from %s", l.TaggedPort.String(), addr.String())
					continue
				}

				l.mutex.Lock()
				if l.nextSession >= sessionList.Len() {
					l.nextSession = 0
				}
				element := sessionList.Front()
				for i := 0; i < l.nextSession; i++ {
					if element == nil {
						log.Warnf("Reached nil element before target index %d for port %s", l.nextSession, l.TaggedPort.String())
						break
					}
					element = element.Next()
				}
				if element == nil {
					log.Warnf("No valid session element found for port %s, ignoring data from %s", l.TaggedPort.String(), addr.String())
					l.mutex.Unlock()
					continue
				}
				comp := element.Value.(*C.SessionIndexCompound)
				l.nextSession++
				l.mutex.Unlock()

				if comp.Session == nil {
					log.Warnf("Session is nil for port %s, ignoring data from %s", l.TaggedPort.String(), addr.String())
					continue
				}
				// Open a new stream for this address
				newStream, err := (*comp.Session).OpenStream(context.Background())
				if err != nil {
					log.Errorf("Failed to open stream for session %v: %v", comp.Session, err)
					continue
				}
				// Write the tagged port to the new stream
				// Write the 3-byte header fully
				hdr := l.TaggedPort.Bytes()
				if len(hdr) != 3 {
					log.Errorf("Invalid tagged port bytes length: %d for %s", len(hdr), l.TaggedPort.String())
					newStream.Close()
					continue
				}
				written := 0
				if ts, ok := newStream.(interface{ ID() uint16 }); ok {
					log.Debugf("server: wrote header for udp stream id=%d port=%s", ts.ID(), l.TaggedPort.String())
				}
				for written < len(hdr) {
					n, werr := newStream.Write(hdr[written:])
					if werr != nil {
						log.Errorf("Failed to write tagged port to new stream: %v", werr)
						newStream.Close()
						continue
					}
					written += n
				}
				// Store the new stream in the source map
				l.SourceMap.Set(key, &newStream)
				log.Infof("Opened new stream for %s on UDP port %s", addr.String(), l.TaggedPort.String())

				// Start a goroutine to handle responses from the stream back to the UDP client
				go l.handleUDPStreamResponse(conn, addr, key, &newStream)

				// Now write the first datagram to the new stream
				_, err = newStream.Write(buf[:n])
				if err != nil {
					log.Errorf("Failed to write data to new stream for %s: %v", addr.String(), err)
					newStream.Close()
					l.SourceMap.Delete(key)
					continue
				}
			}
		}
	}()
	return nil
}

// handleUDPStreamResponse handles responses from the stream back to the UDP client
func (l *UDPPortListener) handleUDPStreamResponse(conn *net.UDPConn, clientAddr *net.UDPAddr, key string, stream *multidialer.Stream) {
	defer func() {
		(*stream).Close()
		l.SourceMap.Delete(key)
		log.Infof("Closed stream for %s on UDP port %s", clientAddr.String(), l.TaggedPort.String())
	}()

	buf := make([]byte, 4096)
	lastActivity := time.Now()
	timeout := 60 * time.Second // 60 second timeout for idle streams

	for {
		// Check for timeout
		if time.Since(lastActivity) > timeout {
			log.Infof("Stream timeout for %s on UDP port %s", clientAddr.String(), l.TaggedPort.String())
			return
		}

		// Set a read deadline to prevent blocking indefinitely
		deadline := time.Now().Add(1 * time.Second)
		if netConn, ok := (*stream).(interface{ SetReadDeadline(time.Time) error }); ok {
			netConn.SetReadDeadline(deadline)
		}

		n, err := (*stream).Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue // Read timeout, check for overall timeout and continue
			}
			if err != io.EOF {
				log.Errorf("Failed to read response from stream for %s: %v", clientAddr.String(), err)
			}
			return
		}

		if n > 0 {
			lastActivity = time.Now()
			// Send the response back to the original UDP client
			_, err = conn.WriteToUDP(buf[:n], clientAddr)
			if err != nil {
				log.Errorf("Failed to send response to UDP client %s: %v", clientAddr.String(), err)
				return
			}
			log.Infof("Sent %d bytes response to %s on UDP port %s", n, clientAddr.String(), l.TaggedPort.String())
		}
	}
}
