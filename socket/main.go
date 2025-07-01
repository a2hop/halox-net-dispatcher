package socket

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DefaultSocketPath = "/var/run/networkd-dispatcher.sock"
	SocketPermissions = 0666
)

type EventType string

const (
	EventInterfaceStateChange EventType = "interface_state_change"
	EventInterfaceAdded       EventType = "interface_added"
	EventInterfaceRemoved     EventType = "interface_removed"
	EventIPAddressAdded       EventType = "ip_address_added"
	EventIPAddressRemoved     EventType = "ip_address_removed"
)

type NetworkEvent struct {
	Type                EventType   `json:"type"`
	Timestamp           time.Time   `json:"timestamp"`
	InterfaceName       string      `json:"interface_name"`
	InterfaceIndex      int         `json:"interface_index,omitempty"`
	OperationalState    string      `json:"operational_state,omitempty"`
	AdministrativeState string      `json:"administrative_state,omitempty"`
	InterfaceType       string      `json:"interface_type,omitempty"`
	Address             []string    `json:"address,omitempty"`
	Gateway             []string    `json:"gateway,omitempty"`
	DNS                 []string    `json:"dns,omitempty"`
	ESSID               string      `json:"essid,omitempty"`
	Data                interface{} `json:"data,omitempty"`
}

type SocketServer struct {
	socketPath string
	listener   net.Listener
	clients    map[net.Conn]bool
	clientsMux sync.RWMutex
	verbose    bool
}

func NewSocketServer(socketPath string, verbose bool) *SocketServer {
	if socketPath == "" {
		socketPath = DefaultSocketPath
	}
	return &SocketServer{
		socketPath: socketPath,
		clients:    make(map[net.Conn]bool),
		verbose:    verbose,
	}
}

func (s *SocketServer) logf(format string, args ...interface{}) {
	if s.verbose {
		log.Printf("SocketServer: "+format, args...)
	}
}

func (s *SocketServer) errorf(format string, args ...interface{}) {
	log.Printf("ERROR SocketServer: "+format, args...)
}

func (s *SocketServer) Start() error {
	s.logf("Attempting to start socket server at: %s", s.socketPath)

	// Remove existing socket file if it exists
	if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
		s.logf("Failed to remove existing socket file: %v", err)
		return fmt.Errorf("failed to remove existing socket: %v", err)
	}

	// Ensure the directory exists
	dir := filepath.Dir(s.socketPath)
	s.logf("Creating directory if needed: %s", dir)
	if dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			s.logf("Failed to create directory %s: %v", dir, err)
			return fmt.Errorf("failed to create socket directory: %v", err)
		}
	}

	// Create Unix domain socket
	s.logf("Creating Unix domain socket...")
	listener, err := net.Listen("unix", s.socketPath)
	if err != nil {
		s.logf("Failed to create Unix socket: %v", err)
		return fmt.Errorf("failed to create socket: %v", err)
	}
	s.listener = listener

	// Set socket permissions
	s.logf("Setting socket permissions to %o", SocketPermissions)
	if err := os.Chmod(s.socketPath, SocketPermissions); err != nil {
		s.errorf("Failed to set socket permissions: %v", err)
		// Don't fail here, just log the error
	}

	s.logf("Socket server started on %s", s.socketPath)

	// Accept connections
	go s.acceptConnections()

	return nil
}

func (s *SocketServer) Stop() error {
	if s.listener != nil {
		s.clientsMux.Lock()
		// Close all client connections
		for client := range s.clients {
			client.Close()
		}
		s.clients = make(map[net.Conn]bool)
		s.clientsMux.Unlock()

		// Close listener
		if err := s.listener.Close(); err != nil {
			return err
		}

		// Remove socket file
		if err := os.Remove(s.socketPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func (s *SocketServer) acceptConnections() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			s.errorf("Failed to accept connection: %v", err)
			return
		}

		s.clientsMux.Lock()
		s.clients[conn] = true
		s.clientsMux.Unlock()

		s.logf("New client connected. Total clients: %d", len(s.clients))

		// Handle client disconnection
		go s.handleClient(conn)
	}
}

func (s *SocketServer) handleClient(conn net.Conn) {
	defer func() {
		s.clientsMux.Lock()
		delete(s.clients, conn)
		clientCount := len(s.clients)
		s.clientsMux.Unlock()

		conn.Close()
		s.logf("Client disconnected. Remaining clients: %d", clientCount)
	}()

	// Keep connection alive and detect disconnection
	buf := make([]byte, 1)
	for {
		conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		_, err := conn.Read(buf)
		if err != nil {
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				// Timeout is expected for idle connections
				continue
			}
			// Client disconnected or error occurred
			break
		}
	}
}

func (s *SocketServer) BroadcastEvent(event *NetworkEvent) {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	eventData, err := json.Marshal(event)
	if err != nil {
		s.errorf("Failed to marshal event: %v", err)
		return
	}

	// Add newline for easier parsing by clients
	eventData = append(eventData, '\n')

	s.clientsMux.RLock()
	clients := make([]net.Conn, 0, len(s.clients))
	for client := range s.clients {
		clients = append(clients, client)
	}
	s.clientsMux.RUnlock()

	s.logf("Broadcasting event to %d clients: %s", len(clients), event.Type)

	// Broadcast to all clients
	for _, client := range clients {
		go func(c net.Conn) {
			c.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := c.Write(eventData); err != nil {
				s.logf("Failed to send event to client: %v", err)
				// Client will be removed when handleClient detects the disconnection
			}
		}(client)
	}
}

// Convenience methods for creating specific event types
func (s *SocketServer) BroadcastStateChange(ifaceName string, ifaceIndex int, operState, adminState, ifaceType string, data interface{}) {
	event := &NetworkEvent{
		Type:                EventInterfaceStateChange,
		InterfaceName:       ifaceName,
		InterfaceIndex:      ifaceIndex,
		OperationalState:    operState,
		AdministrativeState: adminState,
		InterfaceType:       ifaceType,
		Data:                data,
	}
	s.BroadcastEvent(event)
}

func (s *SocketServer) BroadcastInterfaceAdded(ifaceName string, ifaceIndex int, ifaceType string) {
	event := &NetworkEvent{
		Type:           EventInterfaceAdded,
		InterfaceName:  ifaceName,
		InterfaceIndex: ifaceIndex,
		InterfaceType:  ifaceType,
	}
	s.BroadcastEvent(event)
}

func (s *SocketServer) BroadcastInterfaceRemoved(ifaceName string, ifaceIndex int) {
	event := &NetworkEvent{
		Type:           EventInterfaceRemoved,
		InterfaceName:  ifaceName,
		InterfaceIndex: ifaceIndex,
	}
	s.BroadcastEvent(event)
}

func (s *SocketServer) BroadcastIPAddressAdded(ifaceName string, ifaceIndex int, address string, ifaceType string) {
	event := &NetworkEvent{
		Type:           EventIPAddressAdded,
		InterfaceName:  ifaceName,
		InterfaceIndex: ifaceIndex,
		InterfaceType:  ifaceType,
		Address:        []string{address},
	}
	s.BroadcastEvent(event)
}

func (s *SocketServer) BroadcastIPAddressRemoved(ifaceName string, ifaceIndex int, address string, ifaceType string) {
	event := &NetworkEvent{
		Type:           EventIPAddressRemoved,
		InterfaceName:  ifaceName,
		InterfaceIndex: ifaceIndex,
		InterfaceType:  ifaceType,
		Address:        []string{address},
	}
	s.BroadcastEvent(event)
}
