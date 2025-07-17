package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"neofrp/common/multidialer"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"neofrp/common/config"
	C "neofrp/common/constant"
	P "neofrp/common/protocol"

	"github.com/charmbracelet/log"
	"github.com/quic-go/quic-go"
)

func Run(config *config.ClientConfig) {
	// Initialize the client service with the provided configuration
	log.Debugf("Run using config: %+v", config)
	// First create the master connection to the server
	tlsConfig, err := GetTLSConfig()
	if err != nil {
		log.Errorf("Failed to get TLS config: %v", err)
		return
	}
	session, err := multidialer.Dial(
		context.Background(),
		config.TransportConfig.Protocol,
		net.JoinHostPort(config.TransportConfig.IP, fmt.Sprintf("%d", config.TransportConfig.Port)),
		tlsConfig,
	)
	if err != nil {
		log.Errorf("Failed to connect to server: %v", err)
		return
	}
	log.Infof("Connected to server at %s", session.RemoteAddr())

	// Build the control connection
	ctx := context.Background()
	// Let context be cancelable
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ctx = context.WithValue(ctx, C.ContextLastKeepAliveKey, time.Now())
	controlConn, err := session.OpenStream(ctx)
	if err != nil {
		log.Errorf("Failed to open control stream: %v", err)
		return
	}

	controlHandler := NewControlHandler(config, controlConn)
	err = controlHandler.Handshake()
	if err != nil {
		log.Errorf("Handshake failed: %v", err)
		return
	}

	err = controlHandler.Negotiate()
	if err != nil {
		log.Errorf("Negotiation failed: %v", err)
		return
	}

	log.Infof("Successfully negotiated with server")

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	ctx = context.WithValue(ctx, C.ContextSignalChanKey, sigChan)

	// Create a wait group to keep the client running
	var wg sync.WaitGroup

	// Start handling incoming streams from the server
	wg.Add(1)
	go func() {
		defer wg.Done()
		handleIncomingStreams(ctx, session, config)
	}()

	// Keep the control connection alive and handle control messages
	wg.Add(1)
	go func() {
		defer wg.Done()
		RunControlLoop(ctx, controlConn, session, config)
	}()

	log.Infof("Client is running. Press Ctrl+C to stop.")

	// Wait for either all goroutines to finish or a signal
	go func() {
		wg.Wait()
	}()

	<-sigChan
	log.Infof("Received shutdown signal, stopping client...")
	controlConn.Write([]byte{P.ActionClose})
	time.Sleep(100 * time.Millisecond) // Give some time for the close message to be sent
	cancel()

	// Wait for graceful shutdown with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		log.Infof("Client stopped gracefully")
	case <-time.After(5 * time.Second):
		log.Warnf("Timeout waiting for graceful shutdown")
	}
}

func GetTLSConfig() (*tls.Config, error) {
	return &tls.Config{
		InsecureSkipVerify: true,
		ServerName:         "",             // Empty server name to avoid SNI issues
		NextProtos:         []string{"h3"}, // HTTP/3 for QUIC
		MinVersion:         tls.VersionTLS12,
	}, nil
}

func handleIncomingStreams(ctx context.Context, session multidialer.Session, config *config.ClientConfig) {
	for {
		select {
		case <-ctx.Done():
			log.Infof("Context cancelled, stopping stream handler")
			return
		default:
			// Accept incoming streams from the server
			stream, err := session.AcceptStream(ctx)
			if err != nil {
				var appErr *quic.ApplicationError
				if errors.As(err, &appErr) && appErr.ErrorCode == 0x100 {
					// Remote has closed connection.
					// Cancel the context
					log.Error("Remote closed connection forcefully, stopping stream handler")
					time.Sleep(100 * time.Millisecond)
					ctx.Value(C.ContextSignalChanKey).(chan os.Signal) <- os.Interrupt
					return
				} else {
					log.Errorf("Failed to accept stream: %v", err)
				}
				// Add a small delay to prevent tight loop on persistent errors
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// Handle each stream in a separate goroutine
			go handleStream(ctx, stream, config)
		}
	}
}

func handleStream(ctx context.Context, stream multidialer.Stream, config *config.ClientConfig) {
	defer stream.Close()

	// Read the tagged port from the stream to know which local service to connect to
	taggedPortBytes := make([]byte, 3)
	_, err := io.ReadFull(stream, taggedPortBytes)
	if err != nil {
		log.Errorf("Failed to read tagged port from stream: %v", err)
		return
	}

	// Parse the tagged port
	var portType string
	switch taggedPortBytes[0] {
	case C.PortTypeTCP:
		portType = "tcp"
	case C.PortTypeUDP:
		portType = "udp"
	default:
		log.Errorf("Unknown port type: %d", taggedPortBytes[0])
		return
	}

	serverPort := uint16(taggedPortBytes[1])<<8 | uint16(taggedPortBytes[2])
	log.Infof("Handling stream for %s port %d", portType, serverPort)

	// Find the corresponding local port configuration
	var localPort int
	var found bool
	for _, connConfig := range config.ConnectionConfigs {
		if connConfig.Type == portType && connConfig.ServerPort == int(serverPort) {
			localPort = connConfig.LocalPort
			found = true
			break
		}
	}

	if !found {
		log.Errorf("No local configuration found for %s port %d", portType, serverPort)
		return
	}

	// Connect to the local service
	if portType == "tcp" {
		handleTCPStream(ctx, stream, localPort)
	} else if portType == "udp" {
		handleUDPStream(ctx, stream, localPort)
	}
}

func handleTCPStream(ctx context.Context, stream multidialer.Stream, localPort int) {
	// Connect to the local TCP service
	localConn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		log.Errorf("Failed to connect to local TCP service on port %d: %v", localPort, err)
		return
	}
	defer localConn.Close()

	log.Infof("Connected to local TCP service on port %d", localPort)

	// Create a context for this connection
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start bidirectional data copying
	go func() {
		defer cancel() // Cancel context when one direction fails
		_, err := io.Copy(stream, localConn)
		if err != nil {
			log.Errorf("Error copying from local to stream: %v", err)
		}
	}()

	go func() {
		defer cancel() // Cancel context when one direction fails
		_, err := io.Copy(localConn, stream)
		if err != nil {
			log.Errorf("Error copying from stream to local: %v", err)
		}
	}()

	// Wait for context cancellation (either from parent or connection error)
	<-connCtx.Done()
	log.Infof("TCP connection to port %d closed", localPort)
}

func handleUDPStream(ctx context.Context, stream multidialer.Stream, localPort int) {
	// Create UDP connection to the local service
	localAddr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("127.0.0.1:%d", localPort))
	if err != nil {
		log.Errorf("Failed to resolve local UDP address for port %d: %v", localPort, err)
		return
	}

	// Create UDP connection
	localConn, err := net.DialUDP("udp", nil, localAddr)
	if err != nil {
		log.Errorf("Failed to connect to local UDP service on port %d: %v", localPort, err)
		return
	}
	defer localConn.Close()

	log.Infof("Connected to local UDP service on port %d", localPort)

	// Create a context for this connection
	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Handle stream to local UDP (server -> client -> local service)
	go func() {
		defer cancel()
		buffer := make([]byte, 4096)
		for {
			select {
			case <-connCtx.Done():
				return
			default:
				n, err := stream.Read(buffer)
				if err != nil {
					if err != io.EOF {
						log.Errorf("Error reading from stream: %v", err)
					}
					return
				}
				if n > 0 {
					_, err = localConn.Write(buffer[:n])
					if err != nil {
						log.Errorf("Error writing to local UDP service: %v", err)
						return
					}
				}
			}
		}
	}()

	// Handle local UDP to stream (local service -> client -> server)
	go func() {
		defer cancel()
		buffer := make([]byte, 4096)
		for {
			select {
			case <-connCtx.Done():
				return
			default:
				// Set a read timeout to prevent blocking indefinitely
				localConn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				n, err := localConn.Read(buffer)
				if err != nil {
					if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
						continue // Timeout is expected, continue
					}
					if err != io.EOF {
						log.Errorf("Error reading from local UDP service: %v", err)
					}
					return
				}
				if n > 0 {
					_, err = stream.Write(buffer[:n])
					if err != nil {
						log.Errorf("Error writing to stream: %v", err)
						return
					}
				}
			}
		}
	}()

	// Wait for context cancellation (either from parent or connection error)
	<-connCtx.Done()
	log.Infof("UDP connection to port %d closed", localPort)
}
