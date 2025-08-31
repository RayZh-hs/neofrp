package config

import (
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

type ServerConnectionConfig struct {
	TCPPorts []constant.PortType `json:"tcp_ports,omitempty"`
	UDPPorts []constant.PortType `json:"udp_ports,omitempty"`
}

type ConnectionConfig struct {
	Type       string `json:"type"`
	LocalPort  int    `json:"local_port"`
	ServerIP   string `json:"server_ip"`
	ServerPort int    `json:"server_port"`
}
