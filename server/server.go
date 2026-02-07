package server

import (
	"container/list"
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"neofrp/common/config"
	C "neofrp/common/constant"
	"neofrp/common/multidialer"
	"neofrp/common/parser"
	"neofrp/common/safemap"

	"github.com/charmbracelet/log"
	"github.com/fsnotify/fsnotify"
)

// currentConfig holds the atomically-updated server configuration for hot reload
var currentConfig atomic.Pointer[config.ServerConfig]

// getConfig returns the current configuration (thread-safe)
func getConfig() *config.ServerConfig {
	return currentConfig.Load()
}

// Run initializes the server service with the provided configuration
// configFile is the path to the config file for hot reload support
func Run(cfg *config.ServerConfig, configFile string) {
	log.Debugf("Run using config (tokens redacted): protocol=%s port=%d",
		cfg.TransportConfig.Protocol, cfg.TransportConfig.Port)

	// Store initial config atomically
	currentConfig.Store(cfg)

	tlsConfig, err := GetTLSConfig(&cfg.TransportConfig)
	if err != nil {
		log.Errorf("Failed to get TLS config: %v", err)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register the portmap into ctx
	ctx = context.WithValue(ctx, C.ContextPortMapKey, safemap.NewSafeMap[C.TaggedPort, *C.SessionList]())

	// Setup all the ports; collect closers for graceful shutdown
	portClosers, err := SetupPorts(ctx, cfg)
	if err != nil {
		log.Errorf("Failed to setup ports: %v", err)
		return
	}

	// Start the server with the TLS configuration
	listener, err := multidialer.Listen(
		ctx,
		cfg.TransportConfig.Protocol,
		net.JoinHostPort("0.0.0.0", fmt.Sprintf("%d", cfg.TransportConfig.Port)),
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
				// Use getConfig() to get current config for hot reload support
				go handleSession(ctx, &wg, getConfig(), session)
			}
		}
	}()

	// Set up SIGHUP handler for config hot reload
	sighupChan := make(chan os.Signal, 1)
	signal.Notify(sighupChan, syscall.SIGHUP)
	go handleSighupReload(configFile, sighupChan)

	// Set up file watcher for automatic config reload
	go watchConfigFile(ctx, configFile)

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

// handleSighupReload handles SIGHUP signals to reload configuration
func handleSighupReload(configFile string, sighupChan chan os.Signal) {
	for range sighupChan {
		log.Infof("Received SIGHUP, reloading configuration...")
		reloadConfig(configFile)
	}
}

// watchConfigFile watches the config file for changes and reloads automatically
func watchConfigFile(ctx context.Context, configFile string) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Errorf("Failed to create file watcher: %v", err)
		return
	}
	defer watcher.Close()

	err = watcher.Add(configFile)
	if err != nil {
		log.Errorf("Failed to watch config file %s: %v", configFile, err)
		return
	}
	log.Infof("Watching config file for changes: %s", configFile)

	// Debounce timer to avoid multiple reloads from rapid file changes
	var debounceTimer *time.Timer
	const debounceDelay = 500 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			// React to write or create events (some editors delete and recreate)
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				// Re-add the file in case it was recreated
				if event.Op&fsnotify.Create != 0 {
					watcher.Add(configFile)
				}
				// Debounce: reset timer on each change
				if debounceTimer != nil {
					debounceTimer.Stop()
				}
				debounceTimer = time.AfterFunc(debounceDelay, func() {
					log.Infof("Config file changed, reloading...")
					reloadConfig(configFile)
				})
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("File watcher error: %v", err)
		}
	}
}

// reloadConfig parses, validates, and stores the new configuration
func reloadConfig(configFile string) {
	newConfig, err := parser.ParseServerConfig(configFile)
	if err != nil {
		log.Errorf("Failed to parse config during reload: %v", err)
		return
	}

	if err := parser.ValidateServerConfig(newConfig); err != nil {
		log.Errorf("Invalid config during reload: %v", err)
		return
	}

	// Store the new config atomically
	currentConfig.Store(newConfig)
	log.Infof("Configuration reloaded successfully. New connections will use updated config.")

	// Note: Transport settings (protocol, port, certs) require restart.
	// Port listeners are NOT restarted - only tokens and port access rules are reloaded.
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

	// Register this session in the portmap only for NEGOTIATED ports
	if portMap != nil {
		for _, taggedPort := range controlHandler.NegotiatedPorts {
			if sl, ok := portMap.Get(taggedPort); ok {
				sessionIndex := &C.SessionIndexCompound{
					Session: &session,
					Index:   0,
				}
				element := sl.PushBack(sessionIndex)
				registered = append(registered, portElement{taggedPort: taggedPort, element: element})
				log.Infof("Registered session %s for %s", session.RemoteAddr(), taggedPort.String())
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
	for _, pc := range config.ConnectionConfig.TCPPorts {
		taggedPort := C.TaggedPort{
			PortType: "tcp",
			Port:     pc.Port,
		}
		portMap.Set(taggedPort, C.NewSessionList())

		// Create and start TCP listener
		tcpListener := &TCPPortListener{
			TaggedPort: taggedPort,
			PortMap:    portMap,
		}
		closer, startErr := tcpListener.Start(ctx)
		if startErr != nil {
			return nil, fmt.Errorf("failed to start TCP listener for port %d: %v", pc.Port, startErr)
		}
		closers = append(closers, closer)
		log.Infof("Started TCP listener for port %d", pc.Port)
	}

	// Setup UDP ports
	for _, pc := range config.ConnectionConfig.UDPPorts {
		taggedPort := C.TaggedPort{
			PortType: "udp",
			Port:     pc.Port,
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
			return nil, fmt.Errorf("failed to start UDP listener for port %d: %v", pc.Port, startErr)
		}
		closers = append(closers, closer)
		log.Infof("Started UDP listener for port %d", pc.Port)
	}

	return closers, nil
}
