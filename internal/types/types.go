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
	MsgPlayerUpdate MessageType = "player_update"
	MsgPlayerAction MessageType = "player_action"
	MsgGameState    MessageType = "game_state"
	MsgEnemySpawn   MessageType = "enemy_spawn"
	MsgEnemyUpdate  MessageType = "enemy_update"
	MsgRoomData     MessageType = "room_data"
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
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	X          float64            `json:"x"`
	Y          float64            `json:"y"`
	EnemyType  string             `json:"enemy_type"`
	Health     int                `json:"health"`
	MaxHealth  int                `json:"max_health"`
	Mana       int                `json:"mana"`
	MaxMana    int                `json:"max_mana"`
	TargetID   string             `json:"target_id,omitempty"`
	LastAttack time.Time          `json:"-"`
	ThreatList map[string]float64 `json:"-"` // PlayerID -> threat value
	Weapon     *Weapon            `json:"weapon,omitempty"`
	Strength   int                `json:"strength"`
	Agility    int                `json:"agility"`
	Intellect  int                `json:"intellect"`
	Stamina    int                `json:"stamina"`
}

// Wall represents a wall or boundary in the dungeon
type Wall struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Room represents a dungeon room with walls
type Room struct {
	Walls []Wall `json:"walls"`
}
