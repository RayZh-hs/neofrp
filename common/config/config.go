package config

import (
	"encoding/json"
	"neofrp/common/constant"
)

type ClientConfig struct {
	LogConfig         LogConfig                    `json:"log,omitempty"`
	Token             constant.ClientAuthTokenType `json:"token,omitempty"`
	TransportConfig   ClientTransportConfig        `json:"transport,omitempty"`
	ConnectionConfigs []ConnectionConfig           `json:"connections"`
}

type ServerConfig struct {
	LogConfig        LogConfig                      `json:"log,omitempty"`
	RecognizedTokens []constant.ClientAuthTokenType `json:"recognized_tokens,omitempty"`
	TransportConfig  ServerTransportConfig          `json:"transport,omitempty"`
	ConnectionConfig ServerConnectionConfig         `json:"connections,omitempty"`
}

// Utility Definitions
type LogConfig struct {
	LogLevel string `json:"log_level,omitempty"` // default: "info"
}

type ClientTransportConfig struct {
	Protocol   string `json:"protocol,omitempty"`    // "quic" or "tcp"
	IP         string `json:"server_ip,omitempty"`   // Server IP
	Port       int    `json:"server_port,omitempty"` // Server Port
	CAFile     string `json:"ca_file,omitempty"`     // Path to CA file
	ServerName string `json:"server_name,omitempty"` // SNI
}

type ServerTransportConfig struct {
	Protocol string `json:"protocol,omitempty"`  // "quic" or "tcp"
	Port     int    `json:"port,omitempty"`      // Server Port
	CertFile string `json:"cert_file,omitempty"` // Path to certificate file
	KeyFile  string `json:"key_file,omitempty"`  // Path to key file
}

// PortConfig represents a port with optional per-port tokens
type PortConfig struct {
	Port   constant.PortType              `json:"port"`
	Tokens []constant.ClientAuthTokenType `json:"tokens,omitempty"`
}

// PortConfigList supports both legacy (plain numbers) and new (object) formats
type PortConfigList []PortConfig

func (p *PortConfigList) UnmarshalJSON(data []byte) error {
	// Try to parse as array of mixed types
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	*p = make([]PortConfig, 0, len(raw))
	for _, item := range raw {
		// Try parsing as number first (legacy format)
		var port constant.PortType
		if err := json.Unmarshal(item, &port); err == nil {
			*p = append(*p, PortConfig{Port: port})
			continue
		}
		// Try parsing as PortConfig object (new format)
		var pc PortConfig
		if err := json.Unmarshal(item, &pc); err == nil {
			*p = append(*p, pc)
			continue
		}
	}
	return nil
}

type ServerConnectionConfig struct {
	TCPPorts PortConfigList `json:"tcp_ports,omitempty"`
	UDPPorts PortConfigList `json:"udp_ports,omitempty"`
}

type ConnectionConfig struct {
	Type       string `json:"type"`
	LocalPort  int    `json:"local_port"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}

// GetAllTCPPorts returns all TCP port numbers for backward compatibility
func (c *ServerConnectionConfig) GetAllTCPPorts() []constant.PortType {
	ports := make([]constant.PortType, len(c.TCPPorts))
	for i, pc := range c.TCPPorts {
		ports[i] = pc.Port
	}
	return ports
}

// GetAllUDPPorts returns all UDP port numbers for backward compatibility
func (c *ServerConnectionConfig) GetAllUDPPorts() []constant.PortType {
	ports := make([]constant.PortType, len(c.UDPPorts))
	for i, pc := range c.UDPPorts {
		ports[i] = pc.Port
	}
	return ports
}

// GetPortConfig returns the PortConfig for a given port and type, or nil if not found
func (c *ServerConnectionConfig) GetPortConfig(portType string, port constant.PortType) *PortConfig {
	var list PortConfigList
	switch portType {
	case "tcp":
		list = c.TCPPorts
	case "udp":
		list = c.UDPPorts
	default:
		return nil
	}
	for i := range list {
		if list[i].Port == port {
			return &list[i]
		}
	}
	return nil
}

