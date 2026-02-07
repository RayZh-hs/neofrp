package client

import (
	"context"
	E "errors"
	"fmt"
	"io"
	"os"
	"sync/atomic"
	"time"

	"neofrp/common/config"
	C "neofrp/common/constant"
	"neofrp/common/multidialer"
	P "neofrp/common/protocol"

	"github.com/charmbracelet/log"
)

type ControlHandler struct {
	Config        *config.ClientConfig
	ControlStream multidialer.Stream
}

func NewControlHandler(config *config.ClientConfig, controlStream multidialer.Stream) *ControlHandler {
	return &ControlHandler{
		Config:        config,
		ControlStream: controlStream,
	}
}

func (h *ControlHandler) Handshake() error {
	tokenLen := len(h.Config.Token)
	if tokenLen == 0 || tokenLen > 255 {
		return fmt.Errorf("invalid token length %d", tokenLen)
	}
	msg := []byte{C.Version, byte(tokenLen)}
	msg = append(msg, []byte(h.Config.Token)...)
	if _, err := h.ControlStream.Write(msg); err != nil {
		return fmt.Errorf("handshake write failed: %w", err)
	}
	response := make([]byte, 1)
	if _, err := io.ReadFull(h.ControlStream, response); err != nil {
		return fmt.Errorf("handshake read failed: %w", err)
	}
	switch response[0] {
	case P.ReturnCodeAccepted:
		return nil
	case P.ReturnCodeUnrecognizedToken:
		return E.New("unrecognized token")
	case P.ReturnCodePortInUse:
		return E.New("port in use")
	case P.ReturnCodeOtherError:
		return E.New("other error")
	default:
		return E.New("unknown response code")
	}
}

func (h *ControlHandler) Negotiate() error {
	// Build the port configuration data
	// [LENGTH] [DATA] where DATA is []struct { [TYPE] [PORT] }
	var portData []byte

	// Send ports in ConnectionConfigs order so that response indices match
	for _, connConfig := range h.Config.ConnectionConfigs {
		switch connConfig.Type {
		case "tcp":
			portData = append(portData, C.PortTypeTCP)
		case "udp":
			portData = append(portData, C.PortTypeUDP)
		default:
			continue
		}
		portData = append(portData, byte(connConfig.ServerPort>>8))
		portData = append(portData, byte(connConfig.ServerPort&0xFF))
	}

	// Send the port configuration
	length := len(portData) / 3 // Each port entry is 3 bytes
	if length > 255 {
		return fmt.Errorf("too many port configurations (%d), maximum is 255", length)
	}
	msg := append([]byte{byte(length)}, portData...)
	_, err := h.ControlStream.Write(msg)
	if err != nil {
		return fmt.Errorf("failed to send port configuration: %v", err)
	}

	// Read the response
	// [LENGTH] [RESPONSE]
	lengthBuf := make([]byte, 1)
	_, err = io.ReadFull(h.ControlStream, lengthBuf)
	if err != nil {
		return fmt.Errorf("failed to read response length: %v", err)
	}

	responseLength := int(lengthBuf[0])
	if responseLength == 0 {
		return nil // No ports configured
	}

	responses := make([]byte, responseLength)
	_, err = io.ReadFull(h.ControlStream, responses)
	if err != nil {
		return fmt.Errorf("failed to read port responses: %v", err)
	}

	// Check responses — iterate in the same order as sent
	portIndex := 0
	for _, connConfig := range h.Config.ConnectionConfigs {
		if connConfig.Type != "tcp" && connConfig.Type != "udp" {
			continue
		}
		if portIndex >= len(responses) {
			break
		}

		response := responses[portIndex]
		switch response {
		case P.ReturnCodeAccepted:
			// Port accepted
		case P.ReturnCodePortInUse:
			return fmt.Errorf("port %d (%s) is in use", connConfig.ServerPort, connConfig.Type)
		case P.ReturnCodeOtherError:
			return fmt.Errorf("server error for port %d (%s)", connConfig.ServerPort, connConfig.Type)
		default:
			return fmt.Errorf("unknown response code %d for port %d (%s)", response, connConfig.ServerPort, connConfig.Type)
		}
		portIndex++
	}

	return nil
}

func CancelConnection(ctx context.Context, controlConn multidialer.Stream) {
	log.Debug("Sending active close signal to server")
	controlConn.Write([]byte{P.ActionClose})
	time.Sleep(100 * time.Millisecond)
	if v := ctx.Value(C.ContextSignalChanKey); v != nil {
		if cancelChan, ok := v.(chan os.Signal); ok && cancelChan != nil {
			log.Debug("Sending interrupt signal to cancel connection")
			select {
			case cancelChan <- os.Interrupt:
			default:
				log.Debug("Cancel channel is full or closed, skipping signal")
			}
			return
		}
	}
	log.Warn("No cancel channel found in context, cannot send interrupt signal")
}

func RunControlLoop(ctx context.Context, controlConn multidialer.Stream, session multidialer.Session, config *config.ClientConfig) {
	defer controlConn.Close()

	// Send periodic keep-alive messages to maintain the connection
	keepAliveTicker := time.NewTicker(C.KeepAliveInterval)
	defer keepAliveTicker.Stop()

	// Channel to handle control messages
	controlMsg := make(chan []byte, 10)

	// Start a goroutine to read control messages
	go func() {
		buf := make([]byte, 1024)
		for {
			select {
			case <-ctx.Done():
				return
			default:
				n, err := controlConn.Read(buf)
				if err != nil {
					if err != io.EOF {
						log.Errorf("Error reading from control connection: %v", err)
					}
					return
				}

				if n > 0 {
					// Copy the data to avoid race conditions
					msg := make([]byte, n)
					copy(msg, buf[:n])
					select {
					case controlMsg <- msg:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	// Atomic keep-alive tracking to avoid context layering leak
	var lastKA atomic.Int64
	lastKA.Store(time.Now().UnixNano())
	go func() {
		ticker := time.NewTicker(C.KeepAliveTimeout)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				last := time.Unix(0, lastKA.Load())
				if time.Since(last) > C.KeepAliveTimeout {
					log.Warnf("No keep-alive received in the last %v, closing connection", C.KeepAliveTimeout)
					CancelConnection(ctx, controlConn)
					return
				}
			}
		}
	}()

	// Main control loop
	for {
		select {
		case <-ctx.Done():
			log.Infof("Context cancelled, stopping control connection handler")
			return

		case msg := <-controlMsg:
			log.Debugf("Received control message: %v", msg)
			switch msg[0] {
			case P.ActionKeepAlive:
				lastKA.Store(time.Now().UnixNano())
			case P.ActionClose:
				log.Infof("Received active close action from server, closing connection")
				CancelConnection(ctx, controlConn)
			}

		case <-keepAliveTicker.C:
			// Send keep-alive message
			_, err := controlConn.Write([]byte{P.ActionKeepAlive}) // Simple keep-alive byte
			if err != nil {
				log.Errorf("Failed to send keep-alive: %v", err)
				return
			}
			log.Debugf("Sent keep-alive message")
		}
	}
}
