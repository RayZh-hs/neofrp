package parser

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"neofrp/common/config"
)

// ParseClientConfig reads and parses the client configuration file.
func ParseClientConfig(filePath string) (*config.ClientConfig, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read the file content
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	// Apply template processing
	processedContent, err := ApplyTemplate(content)
	if err != nil {
		return nil, err
	}

	// Parse the processed JSON content
	clientConfig := &config.ClientConfig{}
	// Set default values before decoding
	clientConfig.LogConfig.LogLevel = "info"

	if err := json.Unmarshal(processedContent, clientConfig); err != nil {
		return nil, err
	}

	return clientConfig, nil
}

// ParseServerConfig reads and parses the server configuration file.
func ParseServerConfig(filePath string) (*config.ServerConfig, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	// Read the file content
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	// Apply template processing
	processedContent, err := ApplyTemplate(content)
	if err != nil {
		return nil, err
	}

	// Parse the processed JSON content
	serverConfig := &config.ServerConfig{}
	// Set default values before decoding
	serverConfig.LogConfig.LogLevel = "info"

	if err := json.Unmarshal(processedContent, serverConfig); err != nil {
		return nil, err
	}

	return serverConfig, nil
}

func ValidateClientConfig(config *config.ClientConfig) error {
	if config.Token == "" {
		return fmt.Errorf("token is required")
	}
	if config.TransportConfig.Protocol != "quic" && config.TransportConfig.Protocol != "tcp" {
		return fmt.Errorf("invalid transport protocol: %s. Options are: quic, tcp", config.TransportConfig.Protocol)
	}
	if config.TransportConfig.IP == "" {
		return fmt.Errorf("server IP is required in field 'transport'")
	}
	if config.TransportConfig.Port == 0 {
		return fmt.Errorf("server port is required in field 'transport'")
	}
	if config.TransportConfig.CAFile != "" {
		if _, err := os.Stat(config.TransportConfig.CAFile); os.IsNotExist(err) {
			return fmt.Errorf("ca_file not found: %s", config.TransportConfig.CAFile)
		}
	}
	for i, conn := range config.ConnectionConfigs {
		if conn.Type != "tcp" && conn.Type != "udp" {
			return fmt.Errorf("connection %d: invalid connection type: %s", i, conn.Type)
		}
		if conn.ServerPort == 0 || conn.ServerPort > 65535 {
			return fmt.Errorf("connection %d: server_port must be between 1 and 65535", i)
		}
		if conn.LocalPort == 0 || conn.LocalPort > 65535 {
			return fmt.Errorf("connection %d: local_port must be between 1 and 65535", i)
		}
	}
	return nil
}

func ValidateServerConfig(config *config.ServerConfig) error {
	if config.TransportConfig.Protocol != "quic" && config.TransportConfig.Protocol != "tcp" {
		return fmt.Errorf("invalid transport protocol: %s. Options are: quic, tcp", config.TransportConfig.Protocol)
	}
	if config.TransportConfig.Port == 0 || config.TransportConfig.Port > 65535 {
		return fmt.Errorf("server port must be between 1 and 65535")
	}
	if len(config.RecognizedTokens) == 0 {
		return fmt.Errorf("at least one recognized token is required for the server to start")
	}
	if (config.TransportConfig.CertFile != "" && config.TransportConfig.KeyFile == "") || (config.TransportConfig.CertFile == "" && config.TransportConfig.KeyFile != "") {
		return fmt.Errorf("cert_file and key_file must be provided together")
	}
	if config.TransportConfig.CertFile != "" {
		if _, err := os.Stat(config.TransportConfig.CertFile); os.IsNotExist(err) {
			return fmt.Errorf("cert_file not found: %s", config.TransportConfig.CertFile)
		}
	}
	if config.TransportConfig.KeyFile != "" {
		if _, err := os.Stat(config.TransportConfig.KeyFile); os.IsNotExist(err) {
			return fmt.Errorf("key_file not found: %s", config.TransportConfig.KeyFile)
		}
	}
	for i, port := range config.ConnectionConfig.TCPPorts {
		if port == 0 || port > 65535 {
			return fmt.Errorf("tcp_ports[%d]: port must be between 1 and 65535", i)
		}
	}
	for i, port := range config.ConnectionConfig.UDPPorts {
		if port == 0 || port > 65535 {
			return fmt.Errorf("udp_ports[%d]: port must be between 1 and 65535", i)
		}
	}
	return nil
}
