package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"fastdl"
)

// Standalone backend server for development and headless use.
// The desktop app embeds this same Backend via Wails.
func main() {
	cfg := fastdl.DefaultConfig()

	fmt.Printf("YT Blaze backend v%s\n", fastdl.Version)
	fmt.Printf("Listening on http://%s\n", cfg.Addr())
	fmt.Printf("Downloads folder: %s\n", cfg.OutputDir)

	if err := os.MkdirAll(cfg.OutputDir, 0755); err != nil {
		log.Fatalf("cannot create output dir: %v", err)
	}

	b, err := fastdl.NewBackend(cfg)
	if err != nil {
		log.Fatalf("backend init failed: %v", err)
	}
	defer b.Shutdown()

	// graceful shutdown on Ctrl+C
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("\nshutting down...")
		b.Shutdown()
		os.Exit(0)
	}()

	if err := b.Run(); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
