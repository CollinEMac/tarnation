package game

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// Player represents a player in the game world
type Player struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Class     string  `json:"class"`
	Health    int     `json:"health"`
	MaxHealth int     `json:"max_health"`
}

// Message types (should match server)
type MessageType string

const (
	MsgPlayerJoin   MessageType = "player_join"
	MsgPlayerLeave  MessageType = "player_leave"
	MsgPlayerMove   MessageType = "player_move"
	MsgPlayerAction MessageType = "player_action"
	MsgGameState    MessageType = "game_state"
	MsgError        MessageType = "error"
)

type Message struct {
	Type      MessageType     `json:"type"`
	PlayerID  string          `json:"player_id,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Timestamp int64           `json:"timestamp"`
}

// GameClient handles the client-side game logic
type GameClient struct {
	conn          *websocket.Conn
	players       map[string]*Player
	localPlayerID string
	mutex         sync.RWMutex
	connected     bool
	lastMoveTime  time.Time
	moveThrottle  time.Duration
	messages      []string // For displaying debug info
	shouldClose   bool     // Flag to indicate clean shutdown
}

// NewGameClient creates a new game client instance
func NewGameClient() *GameClient {
	return &GameClient{
		players:      make(map[string]*Player),
		moveThrottle: 50 * time.Millisecond, // Limit movement updates to 20/sec
		messages:     make([]string, 0),
		shouldClose:  false,
	}
}

// ConnectToServer establishes WebSocket connection to game server
func (g *GameClient) ConnectToServer(url string) error {
	log.Printf("DEBUG: Dialing WebSocket to %s", url)
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	g.conn = conn
	g.connected = true
	log.Printf("DEBUG: WebSocket connected, starting message handler")

	// Start message handling goroutine
	go g.handleMessages()

	g.addMessage("Connected to server!")
	log.Printf("DEBUG: ConnectToServer completed successfully")
	return nil
}

// handleMessages processes incoming messages from the server
func (g *GameClient) handleMessages() {
	defer func() {
		g.mutex.Lock()
		g.connected = false
		g.mutex.Unlock()
		if g.conn != nil {
			g.conn.Close()
		}
	}()

	for {
		var msg Message
		err := g.conn.ReadJSON(&msg)
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			g.addMessage("Disconnected from server")
			break
		}

		g.processMessage(msg)
	}
}

// processMessage handles incoming server messages
func (g *GameClient) processMessage(msg Message) {
	switch msg.Type {
	case MsgPlayerJoin:
		var player Player
		if err := json.Unmarshal(msg.Data, &player); err != nil {
			log.Printf("Error unmarshaling player join: %v", err)
			return
		}

		g.mutex.Lock()
		g.players[player.ID] = &player

		// If this is our player (first player we receive), store the ID
		isLocalPlayer := g.localPlayerID == ""
		if isLocalPlayer {
			g.localPlayerID = player.ID
			log.Printf("Local player ID set to: %s", g.localPlayerID)
		}
		g.mutex.Unlock()

		// Add messages outside the mutex lock to avoid deadlock
		if isLocalPlayer {
			g.addMessage(fmt.Sprintf("You joined as %s (%s)", player.Name, player.Class))
		} else {
			g.addMessage(fmt.Sprintf("%s joined the game", player.Name))
		}

	case MsgPlayerLeave:
		g.mutex.Lock()
		var playerName string
		if player, exists := g.players[msg.PlayerID]; exists {
			playerName = player.Name
			delete(g.players, msg.PlayerID)
		}
		g.mutex.Unlock()

		// Add message outside the mutex lock to avoid deadlock
		if playerName != "" {
			g.addMessage(fmt.Sprintf("%s left the game", playerName))
		}

	case MsgPlayerMove:
		var moveData struct {
			X float64 `json:"x"`
			Y float64 `json:"y"`
		}

		if err := json.Unmarshal(msg.Data, &moveData); err != nil {
			log.Printf("Error unmarshaling move data: %v", err)
			return
		}

		g.mutex.Lock()
		if player, exists := g.players[msg.PlayerID]; exists {
			player.X = moveData.X
			player.Y = moveData.Y
		}
		g.mutex.Unlock()

	case MsgPlayerAction:
		g.addMessage(fmt.Sprintf("Player %s used an action", msg.PlayerID[:8]))

	case MsgError:
		g.addMessage(fmt.Sprintf("Server error: %s", string(msg.Data)))

	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

// sendMessage sends a message to the server
func (g *GameClient) sendMessage(msgType MessageType, data interface{}) error {
	if !g.connected || g.conn == nil {
		return fmt.Errorf("not connected to server")
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	msg := Message{
		Type:      msgType,
		Data:      jsonData,
		Timestamp: time.Now().UnixMilli(),
	}

	return g.conn.WriteJSON(msg)
}

// Update implements ebiten.Game interface
func (g *GameClient) Update() error {
	g.mutex.RLock()
	connected := g.connected
	g.mutex.RUnlock()

	if !connected {
		return nil
	}

	// Handle player input
	g.handleInput()
	return nil
}

// Cleanup handles graceful shutdown when window is closed
func (g *GameClient) Cleanup() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if g.connected && g.conn != nil {
		// Send clean disconnect message
		g.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		g.conn.Close()
		g.connected = false
	}
}

// handleInput processes keyboard input for player movement
func (g *GameClient) handleInput() {
	if g.localPlayerID == "" {
		return
	}

	// Throttle movement updates
	if time.Since(g.lastMoveTime) < g.moveThrottle {
		return
	}

	g.mutex.RLock()
	localPlayer, exists := g.players[g.localPlayerID]
	g.mutex.RUnlock()

	if !exists {
		return
	}

	newX, newY := localPlayer.X, localPlayer.Y
	moved := false

	// Basic WASD movement
	moveSpeed := 3.0
	if ebiten.IsKeyPressed(ebiten.KeyW) || ebiten.IsKeyPressed(ebiten.KeyArrowUp) {
		newY -= moveSpeed
		moved = true
	}
	if ebiten.IsKeyPressed(ebiten.KeyS) || ebiten.IsKeyPressed(ebiten.KeyArrowDown) {
		newY += moveSpeed
		moved = true
	}
	if ebiten.IsKeyPressed(ebiten.KeyA) || ebiten.IsKeyPressed(ebiten.KeyArrowLeft) {
		newX -= moveSpeed
		moved = true
	}
	if ebiten.IsKeyPressed(ebiten.KeyD) || ebiten.IsKeyPressed(ebiten.KeyArrowRight) {
		newX += moveSpeed
		moved = true
	}

	// Handle action key (spacebar for now)
	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		g.sendMessage(MsgPlayerAction, map[string]string{
			"action": "basic_attack",
		})
	}

	// Send movement update if player moved
	if moved {
		// Update local position immediately for responsive feel
		g.mutex.Lock()
		localPlayer.X = newX
		localPlayer.Y = newY
		g.mutex.Unlock()

		// Send update to server
		moveData := map[string]float64{
			"x": newX,
			"y": newY,
		}

		if err := g.sendMessage(MsgPlayerMove, moveData); err != nil {
			log.Printf("Error sending move: %v", err)
		}

		g.lastMoveTime = time.Now()
	}
}

// Draw implements ebiten.Game interface
func (g *GameClient) Draw(screen *ebiten.Image) {
	// Clear screen with dark background
	screen.Fill(color.RGBA{0x20, 0x20, 0x20, 0xff})

	g.mutex.RLock()
	connected := g.connected
	playerCount := len(g.players)
	localPlayerID := g.localPlayerID

	// Draw all players first
	for _, player := range g.players {
		g.drawPlayer(screen, player)
	}
	g.mutex.RUnlock()

	// Draw UI on top
	g.drawUI(screen)

	// Debug info
	if !connected {
		ebitenutil.DebugPrintAt(screen, "Disconnected from server", 10, 70)
	} else if playerCount == 0 {
		ebitenutil.DebugPrintAt(screen, "Waiting for player data...", 10, 70)
	} else if localPlayerID == "" {
		ebitenutil.DebugPrintAt(screen, "No local player ID set", 10, 70)
	} else {
		ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Local player: %s", localPlayerID[:8]), 10, 70)
	}
}

// drawPlayer renders a player on screen
func (g *GameClient) drawPlayer(screen *ebiten.Image, player *Player) {
	// Simple colored rectangle for now
	playerColor := color.RGBA{0x80, 0x80, 0xff, 0xff} // Blue for other players
	if player.ID == g.localPlayerID {
		playerColor = color.RGBA{0xff, 0x80, 0x80, 0xff} // Red for local player
	}

	// Draw player as a 20x20 rectangle
	ebitenutil.DrawRect(screen, player.X-10, player.Y-10, 20, 20, playerColor)

	// Draw player name
	ebitenutil.DebugPrintAt(screen, player.Name, int(player.X-20), int(player.Y-25))

	// Draw health bar
	barWidth := 30.0
	barHeight := 4.0
	healthPercent := float64(player.Health) / float64(player.MaxHealth)

	// Background (red)
	ebitenutil.DrawRect(screen, player.X-barWidth/2, player.Y+15, barWidth, barHeight, color.RGBA{0xff, 0x00, 0x00, 0xff})

	// Health (green)
	ebitenutil.DrawRect(screen, player.X-barWidth/2, player.Y+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
}

// drawUI renders the game UI
func (g *GameClient) drawUI(screen *ebiten.Image) {
	// Connection status
	status := "Disconnected"
	if g.connected {
		status = "Connected"
	}
	ebitenutil.DebugPrint(screen, fmt.Sprintf("Status: %s | Players: %d", status, len(g.players)))

	// Controls
	ebitenutil.DebugPrintAt(screen, "Controls: WASD/Arrows to move, Space for action", 10, 30)

	// Recent messages (chat/log)
	if len(g.messages) > 0 {
		ebitenutil.DebugPrintAt(screen, "Recent messages:", 10, 60)
		for i, msg := range g.messages {
			if i >= 5 { // Show only last 5 messages
				break
			}
			ebitenutil.DebugPrintAt(screen, msg, 10, 80+i*15)
		}
	}
}

// Layout implements ebiten.Game interface
func (g *GameClient) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return 800, 600
}

// addMessage adds a message to the message log
func (g *GameClient) addMessage(msg string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	// Add message to beginning of slice
	g.messages = append([]string{fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)}, g.messages...)

	// Keep only last 10 messages
	if len(g.messages) > 10 {
		g.messages = g.messages[:10]
	}
}
