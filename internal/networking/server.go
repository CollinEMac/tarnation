package networking

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"

	"github.com/CollinEMac/tarnation/internal/types"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)


// GameServer manages all connected players and game instances
type GameServer struct {
	players   map[string]*types.Player
	mutex     sync.RWMutex
	upgrader  websocket.Upgrader
	broadcast chan types.Message
}

// NewGameServer creates a new game server instance
func NewGameServer() *GameServer {
	server := &GameServer{
		players:   make(map[string]*types.Player),
		broadcast: make(chan types.Message, 256),
		upgrader: websocket.Upgrader{
			CheckOrigin: func(r *http.Request) bool {
				// Allow all origins for development - restrict in production
				return true
			},
		},
	}

	// Start the broadcast goroutine
	go server.handleBroadcast()

	return server
}

// HandleWebSocket upgrades HTTP connections to WebSocket
func (s *GameServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	// Create new player
	playerID := uuid.New().String()
	player := &types.Player{
		ID:        playerID,
		Name:      "Player " + playerID[:8], // Default name
		X:         400,                      // Default spawn position
		Y:         300,
		Class:     "warrior", // Default class
		Health:    100,
		MaxHealth: 100,
		Conn:      conn,
	}

	// Add player to server
	s.mutex.Lock()
	s.players[playerID] = player
	s.mutex.Unlock()

	log.Printf("Player %s connected", playerID)

	// Send welcome message
	welcomeMsg := types.Message{
		Type:     types.MsgPlayerJoin,
		PlayerID: playerID,
		Data:     s.marshalPlayer(player),
	}

	if err := conn.WriteJSON(welcomeMsg); err != nil {
		log.Printf("Error sending welcome message: %v", err)
		return
	}

	// Send information about all existing players to the new player
	s.mutex.RLock()
	for existingPlayerID, existingPlayer := range s.players {
		// Skip sending the new player their own data again
		if existingPlayerID == playerID {
			continue
		}

		existingPlayerMsg := types.Message{
			Type:     types.MsgPlayerJoin,
			PlayerID: existingPlayerID,
			Data:     s.marshalPlayer(existingPlayer),
		}

		if err := conn.WriteJSON(existingPlayerMsg); err != nil {
			log.Printf("Error sending existing player %s data to new player: %v", existingPlayerID, err)
		}
	}
	s.mutex.RUnlock()

	// Broadcast player join to all other players
	s.broadcast <- types.Message{
		Type:     types.MsgPlayerJoin,
		PlayerID: playerID,
		Data:     s.marshalPlayer(player),
	}

	// Handle player messages
	go s.handlePlayerConnection(player)
}

// handlePlayerConnection processes messages from a specific player
func (s *GameServer) handlePlayerConnection(player *types.Player) {
	defer func() {
		// Clean up when player disconnects
		s.mutex.Lock()
		delete(s.players, player.ID)
		s.mutex.Unlock()

		player.Conn.Close()

		// Broadcast player leave
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerLeave,
			PlayerID: player.ID,
		}

		log.Printf("Player %s disconnected", player.ID)
	}()

	for {
		var msg types.Message
		err := player.Conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error for player %s: %v", player.ID, err)
			}
			break
		}

		// Process the message
		s.handleMessage(player, msg)
	}
}

// handleMessage processes incoming messages from players
func (s *GameServer) handleMessage(player *types.Player, msg types.Message) {
	switch msg.Type {
	case types.MsgPlayerMove:
		var moveData struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}

		if err := json.Unmarshal(msg.Data, &moveData); err != nil {
			log.Printf("Error unmarshaling move data: %v", err)
			return
		}

		// Update player position (add validation here)
		s.mutex.Lock()
		player.X = moveData.X
		player.Y = moveData.Y
		s.mutex.Unlock()

		// Broadcast position update
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerMove,
			PlayerID: player.ID,
			Data:     msg.Data,
		}

	case types.MsgPlayerAction:
		// Handle player actions (abilities, attacks, etc.)
		log.Printf("Player %s used action: %s", player.ID, string(msg.Data))

		// Broadcast action to other players
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerAction,
			PlayerID: player.ID,
			Data:     msg.Data,
		}

	default:
		log.Printf("Unknown message type from player %s: %s", player.ID, msg.Type)
	}
}

// handleBroadcast sends messages to all connected players
func (s *GameServer) handleBroadcast() {
	for msg := range s.broadcast {
		s.mutex.RLock()
		for _, player := range s.players {
			// Don't send message back to the sender for certain message types
			if msg.Type == types.MsgPlayerMove && player.ID == msg.PlayerID {
				continue
			}

			if err := player.Conn.WriteJSON(msg); err != nil {
				log.Printf("Error broadcasting to player %s: %v", player.ID, err)
				// Could implement reconnection logic here
			}
		}
		s.mutex.RUnlock()
	}
}

// GetConnectedPlayers returns current player count
func (s *GameServer) GetConnectedPlayers() int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return len(s.players)
}

// marshalPlayer converts player to JSON for network transmission
func (s *GameServer) marshalPlayer(player *types.Player) json.RawMessage {
	data, _ := json.Marshal(player)
	return data
}
