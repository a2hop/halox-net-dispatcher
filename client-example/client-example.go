package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"halox-net-dispatcher/socket"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	socketPath := socket.DefaultSocketPath
	if len(os.Args) > 1 {
		socketPath = os.Args[1]
	}

	// Connect to the socket
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		log.Fatalf("Failed to connect to socket %s: %v", socketPath, err)
	}
	defer conn.Close()

	fmt.Printf("Connected to networkd-dispatcher socket at %s\n", socketPath)
	fmt.Println("Listening for network events... (Press Ctrl+C to exit)")

	// Handle graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt signal, closing connection...")
		conn.Close()
		os.Exit(0)
	}()

	// Read events from socket
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		line := scanner.Text()

		var event socket.NetworkEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			log.Printf("Failed to parse event: %v", err)
			continue
		}

		// Print event information
		fmt.Printf("\n--- Network Event ---\n")
		fmt.Printf("Type: %s\n", event.Type)
		fmt.Printf("Time: %s\n", event.Timestamp.Format("2006-01-02 15:04:05"))
		fmt.Printf("Interface: %s (index: %d)\n", event.InterfaceName, event.InterfaceIndex)

		if event.OperationalState != "" {
			fmt.Printf("Operational State: %s\n", event.OperationalState)
		}
		if event.AdministrativeState != "" {
			fmt.Printf("Administrative State: %s\n", event.AdministrativeState)
		}
		if event.InterfaceType != "" {
			fmt.Printf("Interface Type: %s\n", event.InterfaceType)
		}
		if event.ESSID != "" {
			fmt.Printf("ESSID: %s\n", event.ESSID)
		}
		if len(event.Address) > 0 {
			fmt.Printf("Addresses: %v\n", event.Address)
		}
		if len(event.Gateway) > 0 {
			fmt.Printf("Gateways: %v\n", event.Gateway)
		}
		if len(event.DNS) > 0 {
			fmt.Printf("DNS: %v\n", event.DNS)
		}
		if event.Data != nil {
			if dataBytes, err := json.MarshalIndent(event.Data, "", "  "); err == nil {
				fmt.Printf("Additional Data:\n%s\n", string(dataBytes))
			}
		}
		fmt.Println("-------------------")
	}

	if err := scanner.Err(); err != nil {
		log.Printf("Error reading from socket: %v", err)
	}
}
