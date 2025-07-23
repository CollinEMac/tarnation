package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/CollinEMac/tarnation/internal/game"
	"github.com/hajimehoshi/ebiten/v2"
)

func main() {
	log.Println("Starting Tarnation client...")
	log.Println("DEBUG: This is the updated version!")

	// Create the game client
	gameClient := game.NewGameClient()
	log.Println("DEBUG: Game client created")

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Handle cleanup in a separate goroutine
	go func() {
		<-sigChan
		log.Println("Received shutdown signal, cleaning up...")
		gameClient.Cleanup()
		os.Exit(0)
	}()

	// Configure Ebitengine
	ebiten.SetWindowSize(800, 600)
	ebiten.SetWindowTitle("Tarnation")
	ebiten.SetWindowResizingMode(ebiten.WindowResizingModeEnabled)

	// Connect to server
	log.Println("DEBUG: Attempting to connect to server...")
	if err := gameClient.ConnectToServer("ws://localhost:8080/ws"); err != nil {
		log.Fatal("Failed to connect to server:", err)
	}
	log.Println("DEBUG: Connected to server successfully!")

	// Ensure cleanup happens when program exits normally
	defer gameClient.Cleanup()

	// Start the game loop
	log.Println("DEBUG: Starting Ebitengine game loop...")
	if err := ebiten.RunGame(gameClient); err != nil {
		log.Printf("Game ended: %v", err)
	}

	log.Println("Client shutting down...")
}
