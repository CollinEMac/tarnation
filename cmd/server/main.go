package main

import (
	"log"
	"net/http"

	"github.com/CollinEMac/tarnation/internal/networking"
)

func main() {
	log.Println("Starting Tarnation server...")

	// Create the game server
	server := networking.NewGameServer()

	// Set up HTTP routes
	http.HandleFunc("/ws", server.HandleWebSocket)

	// Serve static files for development (optional)
	http.Handle("/", http.FileServer(http.Dir("./web/")))

	log.Println("Server listening on :8080")
	log.Println("WebSocket endpoint: ws://localhost:8080/ws")

	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("Server failed to start:", err)
	}
}
