# HaloX Network Dispatcher (Go Implementation)

A Go implementation of networkd-dispatcher, a service that monitors systemd-networkd for network state changes and executes scripts accordingly.

## Building

```bash
go mod tidy
go build -o halox-net-dispatcher .
go build -o client-example ./client/client-example.go  # Optional: build example client
```

## Installation

### Manual Installation

```bash
# Build the binary
go build -o halox-net-dispatcher .

# Install binary and service file
sudo cp halox-net-dispatcher /usr/local/bin/
sudo cp networkd-dispatcher.service /etc/systemd/system/

# Create script directories
sudo mkdir -p /etc/networkd-dispatcher /usr/lib/networkd-dispatcher

# Enable and start the service
sudo systemctl daemon-reload
sudo systemctl enable networkd-dispatcher
sudo systemctl start networkd-dispatcher
```

### Service Management

```bash
# Check service status
sudo systemctl status networkd-dispatcher

# View logs
sudo journalctl -u networkd-dispatcher -f

# Restart service
sudo systemctl restart networkd-dispatcher

# Stop service
sudo systemctl stop networkd-dispatcher

# Disable service
sudo systemctl disable networkd-dispatcher
```

## Usage

### Command Line

```bash
./halox-net-dispatcher [options]
```

### Options

- `-S`: Script directory (default: /etc/networkd-dispatcher:/usr/lib/networkd-dispatcher)
- `-T`: Run startup triggers for existing interfaces
- `-v`: Verbose logging
- `-q`: Quiet mode

## Features

- Monitors systemd-networkd via D-Bus for interface state changes
- Executes scripts based on interface states (operational/administrative)
- **Socket Broadcasting**: Broadcasts network events to multiple clients via Unix domain socket
- Compatible with existing networkd-dispatcher script structure
- Supports wireless interface ESSID detection
- Provides JSON interface data to scripts via environment variables
- Systemd integration with sd_notify

## Socket Broadcasting

The dispatcher creates a Unix domain socket at `/var/run/networkd-dispatcher.sock` that broadcasts network events to multiple connected clients. This allows other processes to consume network state changes in real-time.

### Event Types

- `interface_state_change`: Interface operational or administrative state changed
- `interface_added`: New interface detected
- `interface_removed`: Interface removed (administrative state = "linger")

### Event Format

Events are broadcast as JSON objects, one per line:

```json
{
  "type": "interface_state_change",
  "timestamp": "2025-01-01T17:30:00Z",
  "interface_name": "eth0",
  "interface_index": 2,
  "operational_state": "routable",
  "administrative_state": "configured",
  "interface_type": "ether",
  "address": ["192.168.1.100/24"],
  "gateway": ["192.168.1.1"],
  "dns": ["8.8.8.8"],
  "data": { /* full interface data */ }
}
```

### Example Client Usage

```bash
# Run the example client
./client-example

# Or connect with netcat
nc -U /var/run/networkd-dispatcher.sock

# Or use socat
socat UNIX-CONNECT:/var/run/networkd-dispatcher.sock -
```

### Programmatic Usage

```go
conn, err := net.Dial("unix", "/var/run/networkd-dispatcher.sock")
if err != nil {
    log.Fatal(err)
}
defer conn.Close()

scanner := bufio.NewScanner(conn)
for scanner.Scan() {
    var event socket.NetworkEvent
    json.Unmarshal(scanner.Bytes(), &event)
    // Process event...
}
```

## Script Environment Variables

Scripts receive the following environment variables:

- `IFACE`: Interface name
- `STATE`: Current state
- `ADDR`: Primary IP address
- `IP_ADDRS`: Space-separated IPv4 addresses
- `IP6_ADDRS`: Space-separated IPv6 addresses
- `ESSID`: Wireless ESSID (if applicable)
- `AdministrativeState`: Administrative state
- `OperationalState`: Operational state
- `json`: Complete interface data as JSON
