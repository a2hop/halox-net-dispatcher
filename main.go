package main

import (
	"context"
	"flag"
	client "halox-net-dispatcher/client"
	disp "halox-net-dispatcher/dispatcher"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
	// Check if this is a listen command
	if len(os.Args) > 1 && os.Args[1] == "listen" {
		// Parse listen subcommand flags
		listenFlags := flag.NewFlagSet("listen", flag.ExitOnError)
		socketPath := listenFlags.String("s", "", "Socket path (default: /var/run/networkd-dispatcher.sock)")
		listenFlags.Parse(os.Args[2:])

		if err := client.RunClient(*socketPath); err != nil {
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
