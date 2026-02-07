<div align="center">
  <img src="./public/icon.png" alt="neofrp-logo" width="180">
</div>

# Neofrp

A modern, high-performance reverse proxy implementation in Go, focusing on speed, multiplexing, and secure transport protocols.

[![Go Version](https://img.shields.io/badge/go-%3E%3D1.23-blue.svg)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-green.svg)](./public/license.pdf)

## 🚀 Features

- **High Performance**: Built with Go for concurrent processing and low latency. TCP performance 80% faster than FRP under the same setting.
- **Secure Communication**: Enforced secure communication via QUIC(udp) and TLS(tcp) transport
- **Port Multiplexing**: Allowing for efficient handling of multiple TCP/UDP port forwarding
- **Easy Configuration**: JSON-based configuration files
- **Comprehensive Logging**: Structured logging with configurable levels

## 📋 Table of Contents

- [Installation](#-installation)
- [Quick Start](#-quick-start)
- [Configuration](#-configuration)
- [Contributing](#-contributing)

## 🔧 Installation

### Prerequisites

- Go 1.23 or higher
- Git (for cloning the repository)

### Build from Source

```bash
# Clone the repository
git clone https://github.com/RayZh-hs/neofrp/
cd neofrp

# Build both client and server
make

# Or build individually
make bin/frpc  # Client only
make bin/frps  # Server only
```

The binaries will be created in the `bin/` directory:
- `bin/frpc` - Client binary
- `bin/frps` - Server binary

## 🚀 Quick Start

### 1. Server Setup

Create a server configuration file `server.json`:

```json
{
    "log": {
        "log_level": "info"
    },
    "recognized_tokens": [
        "your_secret_token"
    ],
    "transport": {
        "protocol": "quic",
        "port": 3400
    },
    "connections": {
        "tcp_ports": [35560, 35561],
        "udp_ports": [35562]
    }
}
```

Start the server:
```bash
./bin/frps -c server.json
```

### 2. Client Setup

Create a client configuration file `client.json`:

```json
{
    "log": {
        "log_level": "info"
    },
    "token": "your_secret_token",
    "transport": {
        "protocol": "quic",
        "server_ip": "your.server.ip",
        "server_port": 3400
    },
    "connections": [
        {
            "type": "tcp",
            "local_port": 22,
            "server_port": 35560
        },
        {
            "type": "tcp",
            "local_port": 80,
            "server_port": 35561
        }
    ]
}
```

Start the client:
```bash
./bin/frpc -c client.json
```

Now you can access your local services through the server:
- `your.server.ip:35560` → `localhost:22` (SSH)
- `your.server.ip:35561` → `localhost:80` (HTTP)

You can forward any local port, TCP or UDP, by editing the `client.json` file and ensuring that the corresponding ports are exposed in the server configuration.

## ⚙️ Configuration

### Server Configuration

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `log.log_level` | string | Logging level (debug, info, warn, error, fatal) | "info" |
| `recognized_tokens` | array | List of valid client tokens (global access to all ports) | [] |
| `transport.protocol` | string | Transport protocol ("quic" or "tcp") | "quic" |
| `transport.port` | number | Server listening port | Required |
| `transport.cert_file` | string | Path to TLS certificate file | (self-signed) |
| `transport.key_file` | string | Path to TLS key file | (self-signed) |
| `connections.tcp_ports` | array | TCP ports to expose (can be numbers or objects with per-port tokens) | [] |
| `connections.udp_ports` | array | UDP ports to expose (can be numbers or objects with per-port tokens) | [] |

#### Per-Port Token Authentication

For enhanced security, you can assign specific tokens to specific ports. This prevents clients with different tokens from accessing each other's ports:

```json
{
    "recognized_tokens": [],
    "connections": {
        "tcp_ports": [
            { "port": 35560, "tokens": ["user_a_token"] },
            { "port": 35561, "tokens": ["user_b_token"] },
            35562
        ]
    }
}
```

- Tokens in `recognized_tokens` (global tokens) have access to **all** ports
- Per-port tokens only grant access to their specific port
- Ports without tokens (legacy format) are accessible by any authenticated user
- Both formats can be mixed in the same config

#### Hot Reload

The server automatically watches the config file and reloads when changes are detected. You can also manually trigger a reload:

```bash
kill -SIGHUP $(pgrep frps)
```

**What gets reloaded:**
- Tokens (both global and per-port)
- Port access rules

**What requires restart:**
- Transport settings (protocol, port, certificates)
- Adding/removing port listeners

You can use templating to register a range of ports:

```json
{{ expand "1000-1200" }}
```

This will expand to a list of ports from 1000 to 1200, inclusive, connected by commas.

### Client Configuration

| Field | Type | Description | Default |
|-------|------|-------------|---------|
| `log.log_level` | string | Logging level (debug, info, warn, error, fatal) | "info" |
| `token` | string | Authentication token | Required |
| `transport.protocol` | string | Transport protocol ("quic" or "tcp") | "quic" |
| `transport.server_ip` | string | Server IP address | Required |
| `transport.server_port` | number | Server port | Required |
| `transport.ca_file` | string | Path to CA file for server verification | (none) |
| `transport.server_name` | string | Server name for SNI | `transport.server_ip` |
| `connections` | array | Connection configurations | Required |

#### Connection Configuration

| Field | Type | Description |
|-------|------|-------------|
| `type` | string | Connection type ("tcp" or "udp") |
| `local_port` | number | Local port to forward from |
| `server_port` | number | Server port to forward to |

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

### Development Setup

```bash
# Clone and build
git clone https://github.com/RayZh-hs/neofrp/
cd neofrp
make build

# Clean build artifacts
make clean
```

## 📄 License

This project is licensed under the MIT License. See the [LICENSE](./LICENSE) file for details.
