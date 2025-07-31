package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/CollinEMac/tarnation/internal/networking"
)

func main() {
	log.Println("Starting Tarnation server...")

	// Create the game server
	gameServer := networking.NewGameServer()

	// Set up HTTP routes
	http.HandleFunc("/ws", gameServer.HandleWebSocket)

	// Serve static files for development (optional)
	http.Handle("/", http.FileServer(http.Dir("./web/")))

	// Create HTTP server with proper configuration
	server := &http.Server{
		Addr: ":8080",
	}

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Start server in a goroutine
	go func() {
		log.Println("Server listening on :8080")
		log.Println("WebSocket endpoint: ws://localhost:8080/ws")
		log.Println("Press Ctrl+C to stop the server")
		
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal("Server failed to start:", err)
		}
	}()

	// Wait for signal
	<-sigChan
	log.Println("Received shutdown signal, shutting down server...")

	// Create a context with timeout for shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Shutdown server gracefully
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("Server shutdown error: %v", err)
	} else {
		log.Println("Server shutdown complete")
	}
}
