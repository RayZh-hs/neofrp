package constant

import (
	"time"

	"github.com/charmbracelet/log"
)

const (
	Version           = byte(0x03)
	ControlChannelID  = ChannelIdentifier(0x0)
	ControlUnitALPN   = "nfrp"
	KeepAliveTimeout  = 30 * time.Second
	KeepAliveInterval = 5 * time.Second
	RelayInterval     = 100 * time.Millisecond

	PortTypeTCP byte = 0x01
	PortTypeUDP byte = 0x02

	ContextPortMapKey       = ContextKeyType(0x01)
	ContextLastKeepAliveKey = ContextKeyType(0x02)
	ContextSignalChanKey    = ContextKeyType(0x03)
	// ContextSessionCancelKey stores a context.CancelFunc for per-session cancellation on server side
	ContextSessionCancelKey = ContextKeyType(0x04)
)

var LogLevelMap = map[string]log.Level{
	"debug": log.DebugLevel,
	"info":  log.InfoLevel,
	"warn":  log.WarnLevel,
	"error": log.ErrorLevel,
	"fatal": log.FatalLevel,
}
