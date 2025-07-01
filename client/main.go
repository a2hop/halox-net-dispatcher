package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"halox-net-dispatcher/socket"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func printTableHeader() {
	fmt.Println(strings.Repeat("-", 140))
	fmt.Printf("%-8s %-25s %-8s %-5s %-15s %-15s %-30s\n",
		"TIME", "EVENT", "IFACE", "INDEX", "OPER_STATE", "ADMIN_STATE", "DETAILS")
	fmt.Println(strings.Repeat("-", 140))
}

func printTableRow(event socket.NetworkEvent) {
	timeStr := event.Timestamp.Format("15:04:05")
	details := ""

	switch event.Type {
	case "ip_address_added", "ip_address_removed":
		if len(event.Address) > 0 {
			details = fmt.Sprintf("IP: %s", event.Address[0])
		}
	case "interface_added":
		details = fmt.Sprintf("Type: %s", event.InterfaceType)
	case "interface_removed":
		details = "Removed"
	case "state_change":
		if event.ESSID != "" {
			details = fmt.Sprintf("ESSID: %s", event.ESSID)
		} else if len(event.Address) > 0 {
			details = fmt.Sprintf("Addrs: %d", len(event.Address))
		}
	}

	fmt.Printf("%-8s %-25s %-8s %-5d %-15s %-15s %-30s\n",
		timeStr, event.Type, event.InterfaceName, event.InterfaceIndex,
		event.OperationalState, event.AdministrativeState, details)
}

func RunClient(socketPath string) error {
	if socketPath == "" {
		socketPath = socket.DefaultSocketPath
	}

	// Connect to the socket
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to socket %s: %v", socketPath, err)
	}
	defer conn.Close()

	fmt.Printf("Connected to networkd-dispatcher socket at %s\n", socketPath)
	fmt.Println("Listening for network events... (Press Ctrl+C to exit)\n")

	printTableHeader()

	// Handle graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt signal, shutting down...")
		cancel()
	}()

	// Channel for events from scanner goroutine
	eventChan := make(chan socket.NetworkEvent)
	errorChan := make(chan error, 1)

	// Read events from socket in separate goroutine
	go func() {
		defer close(eventChan)
		scanner := bufio.NewScanner(conn)
		for scanner.Scan() {
			line := scanner.Text()
			var event socket.NetworkEvent
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				log.Printf("Failed to parse event: %v", err)
				continue
			}
			select {
			case eventChan <- event:
			case <-ctx.Done():
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case errorChan <- err:
			case <-ctx.Done():
			}
		}
	}()

	// Main event loop
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-eventChan:
			if !ok {
				return nil // Scanner goroutine finished
			}
			printTableRow(event)
		case err := <-errorChan:
			return fmt.Errorf("error reading from socket: %v", err)
		}
	}
}
