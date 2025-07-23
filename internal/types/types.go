package types

import (
	"encoding/json"

	"github.com/gorilla/websocket"
)

// MessageType represents the type of message being sent
type MessageType string

const (
	MsgPlayerJoin   MessageType = "player_join"
	MsgPlayerLeave  MessageType = "player_leave"
	MsgPlayerMove   MessageType = "player_move"
	MsgPlayerAction MessageType = "player_action"
	MsgGameState    MessageType = "game_state"
	MsgError        MessageType = "error"
)

// Message represents all communication between client and server
type Message struct {
	Type      MessageType     `json:"type"`
	PlayerID  string          `json:"player_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp"`
}

// Player represents a player in the game world
type Player struct {
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	X         float64         `json:"x"`
	Y         float64         `json:"y"`
	Class     string          `json:"class"`
	Health    int             `json:"health"`
	MaxHealth int             `json:"max_health"`
	Conn      *websocket.Conn `json:"-"` // Only used on server side
}