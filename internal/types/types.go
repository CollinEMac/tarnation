package types

import (
	"encoding/json"
	"time"

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
	MsgEnemySpawn   MessageType = "enemy_spawn"
	MsgEnemyUpdate  MessageType = "enemy_update"
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
	Mana      int             `json:"mana"`
	MaxMana   int             `json:"max_mana"`
	Conn      *websocket.Conn `json:"-"` // Only used on server side
	Target    int             `json:"target"`
	Weapon    *Weapon         `json:"weapon,omitempty"`
	Strength  int             `json:"strength"`
	Agility   int             `json:"agility"`
	Intellect int             `json:"intellect"`
	Stamina   int             `json:"stamina"`
}

// Weapon represents the weapon equipped by the player or enemy
type Weapon struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Damage     int           `json:"damage"`
	Range      int           `json:"range"`
	WeaponType string        `json:"weapon_type"`
	Delay      time.Duration `'json:"delay"`
}

// Enemy represent a targetable enemy in the game
type Enemy struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	EnemyType string  `json:"enemy_type"`
	Health    int     `json:"health"`
	MaxHealth int     `json:"max_health"`
	Mana      int             `json:"mana"`
	MaxMana   int             `json:"max_mana"`
	Target    int     `json:"target"`
	Weapon    *Weapon `json:"weapon,omitempty"`
	Strength  int             `json:"strength"`
	Agility   int             `json:"agility"`
	Intellect int             `json:"intellect"`
	Stamina   int             `json:"stamina"`
}
