package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	disp "halox-net-dispatcher/dispatcher"
	"halox-net-dispatcher/socket"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
)

func printTableHeader() {
	fmt.Println(strings.Repeat("-", 120))
	fmt.Printf("%-20s %-15s %-8s %-12s %-15s %-15s %-20s\n",
		"TIME", "EVENT", "IFACE", "INDEX", "OPER_STATE", "ADMIN_STATE", "DETAILS")
	fmt.Println(strings.Repeat("-", 120))
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

	fmt.Printf("%-20s %-15s %-8s %-12d %-15s %-15s %-20s\n",
		timeStr, event.Type, event.InterfaceName, event.InterfaceIndex,
		event.OperationalState, event.AdministrativeState, details)
}

func runClient(socketPath string) error {
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

		printTableRow(event)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading from socket: %v", err)
	}

	return nil
}

func main() {
	// Check if this is a listen command
	if len(os.Args) > 1 && os.Args[1] == "listen" {
		// Parse listen subcommand flags
		listenFlags := flag.NewFlagSet("listen", flag.ExitOnError)
		socketPath := listenFlags.String("s", "", "Socket path (default: /var/run/networkd-dispatcher.sock)")
		listenFlags.Parse(os.Args[2:])

		if err := runClient(*socketPath); err != nil {
			log.Fatalf("Client failed: %v", err)
		}
		return
	}

	// Original dispatcher functionality
	var (
		scriptDir          = flag.String("S", disp.DefaultScriptDir, "Script directory")
		runStartupTriggers = flag.Bool("T", false, "Run startup triggers")
		verbose            = flag.Bool("v", false, "Verbose logging")
		quiet              = flag.Bool("q", false, "Quiet mode")
		socketPath         = flag.String("socket", "", "Socket path (default: /var/run/networkd-dispatcher.sock)")
	)
	flag.Parse()

	if *quiet {
		log.SetOutput(os.Stderr)
	}

	// Check for networkctl
	if _, err := exec.LookPath("networkctl"); err != nil {
		log.Fatal("networkctl command not found")
	}

	dispatcher := disp.NewDispatcher(*scriptDir, *socketPath, *verbose && !*quiet)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigChan
		log.Println("Received shutdown signal")
		cancel()
	}()

	if err := dispatcher.Run(ctx, *runStartupTriggers); err != nil && err != context.Canceled {
		log.Fatalf("Dispatcher failed: %v", err)
	}

	log.Println("Shutdown complete")
}
