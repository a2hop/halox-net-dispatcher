package main

import (
	"context"
	"flag"
	disp "halox-net-dispatcher/dispatcher"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func main() {
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
