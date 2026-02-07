package server

import (
	"container/list"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"neofrp/common/config"
	C "neofrp/common/constant"
	"neofrp/common/multidialer"
	"neofrp/common/safemap"

	"github.com/charmbracelet/log"
)

// Run initializes the server service with the provided configuration
func Run(config *config.ServerConfig) {
	log.Debugf("Run using config (tokens redacted): protocol=%s port=%d tcp_ports=%v udp_ports=%v",
		config.TransportConfig.Protocol, config.TransportConfig.Port,
		config.ConnectionConfig.TCPPorts, config.ConnectionConfig.UDPPorts)
	tlsConfig, err := GetTLSConfig(&config.TransportConfig)
	if err != nil {
		log.Errorf("Failed to get TLS config: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register the portmap into ctx
	ctx = context.WithValue(ctx, C.ContextPortMapKey, safemap.NewSafeMap[C.TaggedPort, *C.SessionList]())

	// Setup all the ports; collect closers for graceful shutdown
	portClosers, err := SetupPorts(ctx, config)
	if err != nil {
		log.Errorf("Failed to setup ports: %v", err)
		return
	}

	// Start the server with the TLS configuration
	listener, err := multidialer.Listen(
		ctx,
		config.TransportConfig.Protocol,
		net.JoinHostPort("0.0.0.0", fmt.Sprintf("%d", config.TransportConfig.Port)),
		tlsConfig,
	)
	if err != nil {
		log.Errorf("Failed to start listener: %v", err)
		return
	}
	defer listener.Close()
	log.Infof("Server listening on %s", listener.Addr())

	var wg sync.WaitGroup

	// Accept sessions from clients
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				session, err := listener.AcceptSession()
				if err != nil {
					if ctx.Err() == nil {
						log.Errorf("Failed to accept session: %v", err)
					}
					continue
				}
				log.Infof("Accepted connection from %s", session.RemoteAddr())
				wg.Add(1)
				go handleSession(ctx, &wg, config, session)
			}
		}
	}()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	log.Infof("Received shutdown signal, stopping server...")
	cancel()

	// Close all port listeners so their goroutines can exit
	for _, closer := range portClosers {
		closer()
	}

	wg.Wait()
	log.Infof("Server stopped gracefully")
}

func handleSession(ctx context.Context, wg *sync.WaitGroup, config *config.ServerConfig, session multidialer.Session) {
	defer wg.Done()
	// Create a cancellable context for this session
	sessionCtx, sessionCancel := context.WithCancel(ctx)
	// Store cancel func in context so helper utilities (CancelConnection) can terminate session without relying on signal channel
	sessionCtx = context.WithValue(sessionCtx, C.ContextSessionCancelKey, sessionCancel)
	defer sessionCancel()

	// Track registered elements for cleanup
	type portElement struct {
		taggedPort C.TaggedPort
		element    *list.Element
	}
	var registered []portElement

	portMap := ctx.Value(C.ContextPortMapKey).(*safemap.SafeMap[C.TaggedPort, *C.SessionList])

	// Cleanup function to ensure resources are properly released.
	// Runs on ALL exit paths (including negotiate failure).
	defer func() {
		// Clean up session from portmap
		if portMap != nil {
			for _, pe := range registered {
				if sl, ok := portMap.Get(pe.taggedPort); ok {
					sl.Remove(pe.element)
					log.Infof("Unregistered session %s from %s", session.RemoteAddr(), pe.taggedPort.String())
				}
			}
		}

		// Close the session gracefully
		err := session.Close(fmt.Errorf("session closed by server"))
		if err != nil {
			log.Errorf("Failed to close session: %v", err)
		} else {
			log.Infof("Session %s closed successfully", session.RemoteAddr())
		}

		log.Infof("Session cleanup completed for %s", session.RemoteAddr())
	}()

	// Open a control stream for the session
	controlConn, err := session.AcceptStream(sessionCtx)
	if err != nil {
		log.Errorf("Failed to accept control stream: %v", err)
		return
	}
	defer controlConn.Close()

	// Read the client handshake
	controlHandler := NewControlHandler(config, controlConn)
	err = controlHandler.Handshake()
	if err != nil {
		log.Errorf("Handshake failed: %v", err)
		return
	}
	log.Infof("Handshake successful with client %s", session.RemoteAddr())

	err = controlHandler.Negotiate()
	if err != nil {
		log.Errorf("Negotiation failed: %v", err)
		return
	}

	// Register this session in the portmap AFTER successful negotiation
	if portMap != nil {
		// Register session for TCP ports
		for _, port := range config.ConnectionConfig.TCPPorts {
			taggedPort := C.TaggedPort{
				PortType: "tcp",
				Port:     C.PortType(port),
			}
			if sl, ok := portMap.Get(taggedPort); ok {
				sessionIndex := &C.SessionIndexCompound{
					Session: &session,
					Index:   0,
				}
				element := sl.PushBack(sessionIndex)
				registered = append(registered, portElement{taggedPort: taggedPort, element: element})
				log.Infof("Registered session %s for TCP port %d", session.RemoteAddr(), port)
			}
		}

		// Register session for UDP ports
		for _, port := range config.ConnectionConfig.UDPPorts {
			taggedPort := C.TaggedPort{
				PortType: "udp",
				Port:     C.PortType(port),
			}
			if sl, ok := portMap.Get(taggedPort); ok {
				sessionIndex := &C.SessionIndexCompound{
					Session: &session,
					Index:   0,
				}
				element := sl.PushBack(sessionIndex)
				registered = append(registered, portElement{taggedPort: taggedPort, element: element})
				log.Infof("Registered session %s for UDP port %d", session.RemoteAddr(), port)
			}
		}
	}

	// Setup the control feedback loop
	go RunControlLoop(sessionCtx, controlConn, session, config)

	// Wait for cancellation signal
	<-sessionCtx.Done()
	log.Infof("Closing session %s due to context cancellation", session.RemoteAddr())
}

// SetupPorts creates and starts port listeners. Returns a slice of closer
// functions that should be called on shutdown to stop the listener goroutines.
func SetupPorts(ctx context.Context, config *config.ServerConfig) (closers []func(), err error) {
	portMap := ctx.Value(C.ContextPortMapKey).(*safemap.SafeMap[C.TaggedPort, *C.SessionList])
	if portMap == nil {
		return nil, fmt.Errorf("portmap not found in context")
	}

	// Setup TCP ports
	for _, port := range config.ConnectionConfig.TCPPorts {
		taggedPort := C.TaggedPort{
			PortType: "tcp",
			Port:     C.PortType(port),
		}
		portMap.Set(taggedPort, C.NewSessionList())

		// Create and start TCP listener
		tcpListener := &TCPPortListener{
			TaggedPort: taggedPort,
			PortMap:    portMap,
		}
		closer, startErr := tcpListener.Start(ctx)
		if startErr != nil {
			return nil, fmt.Errorf("failed to start TCP listener for port %d: %v", port, startErr)
		}
		closers = append(closers, closer)
		log.Infof("Started TCP listener for port %d", port)
	}

	// Setup UDP ports
	for _, port := range config.ConnectionConfig.UDPPorts {
		taggedPort := C.TaggedPort{
			PortType: "udp",
			Port:     C.PortType(port),
		}
		portMap.Set(taggedPort, C.NewSessionList())

		// Create and start UDP listener
		udpListener := &UDPPortListener{
			TaggedPort: taggedPort,
			PortMap:    portMap,
			SourceMap:  safemap.NewSafeMap[string, *multidialer.Stream](),
		}
		closer, startErr := udpListener.Start(ctx)
		if startErr != nil {
			return nil, fmt.Errorf("failed to start UDP listener for port %d: %v", port, startErr)
		}
		closers = append(closers, closer)
		log.Infof("Started UDP listener for port %d", port)
	}

	return closers, nil
}
