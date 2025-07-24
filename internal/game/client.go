package game

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"sync"
	"time"

	"github.com/CollinEMac/tarnation/internal/types"
	"github.com/gorilla/websocket"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
)

// GameClient handles the client-side game logic
type GameClient struct {
	conn          *websocket.Conn
	players       map[string]*types.Player
	enemies       map[string]*types.Enemy
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
		players:      make(map[string]*types.Player),
		enemies:      make(map[string]*types.Enemy),
		moveThrottle: 16 * time.Millisecond, // Limit movement updates to ~60/sec to match render loop
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
		var msg types.Message
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
func (g *GameClient) processMessage(msg types.Message) {
	switch msg.Type {
	case types.MsgPlayerJoin:
		var player types.Player
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

	case types.MsgPlayerLeave:
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

	case types.MsgPlayerMove:
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

	case types.MsgPlayerAction:
		g.addMessage(fmt.Sprintf("Player %s used an action", msg.PlayerID[:8]))

	case types.MsgEnemySpawn:
		var enemy types.Enemy
		if err := json.Unmarshal(msg.Data, &enemy); err != nil {
			log.Printf("Error unmarshaling enemy spawn: %v", err)
			return
		}

		g.mutex.Lock()
		g.enemies[enemy.ID] = &enemy
		g.mutex.Unlock()

		g.addMessage(fmt.Sprintf("Enemy %s spawned", enemy.Name))

	case types.MsgEnemyUpdate:
		var enemy types.Enemy
		if err := json.Unmarshal(msg.Data, &enemy); err != nil {
			log.Printf("Error unmarshaling enemy update: %v", err)
			return
		}

		g.mutex.Lock()
		if existingEnemy, exists := g.enemies[enemy.ID]; exists {
			existingEnemy.X = enemy.X
			existingEnemy.Y = enemy.Y
			existingEnemy.Health = enemy.Health
		}
		g.mutex.Unlock()

	case types.MsgError:
		g.addMessage(fmt.Sprintf("Server error: %s", string(msg.Data)))

	default:
		log.Printf("Unknown message type: %s", msg.Type)
	}
}

// sendMessage sends a message to the server
func (g *GameClient) sendMessage(msgType types.MessageType, data interface{}) error {
	if !g.connected || g.conn == nil {
		return fmt.Errorf("not connected to server")
	}

	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	msg := types.Message{
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

	// Throttle movement updates - but allow more frequent updates for smoother feel
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
		g.sendMessage(types.MsgPlayerAction, map[string]string{
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

		if err := g.sendMessage(types.MsgPlayerMove, moveData); err != nil {
			log.Printf("Error sending move: %v", err)
		}

		g.lastMoveTime = time.Now()
	}
}

// Draw implements ebiten.Game interface
func (g *GameClient) Draw(screen *ebiten.Image) {
	// Clear screen with dark background
	screen.Fill(color.RGBA{0x20, 0x20, 0x20, 0xff})

	// Copy enemy data to avoid holding locks during rendering
	g.mutex.RLock()

	// Create a snapshot of players to avoid holding lock during draw
	enemySnapshot := make([]*types.Enemy, 0, len(g.enemies))
	for _, enemy := range g.enemies {
		// Create copy of enemy data
		enemyCopy := *enemy
		enemySnapshot = append(enemySnapshot, &enemyCopy)
	}
	g.mutex.RUnlock()

	// Draw all enemies without holding any locks
	for _, enemy := range enemySnapshot {
		g.drawEnemy(screen, enemy)
	}

	// Players should be drawn last so they are in front
	// Copy player data to avoid holding locks during rendering
	g.mutex.RLock()
	connected := g.connected
	playerCount := len(g.players)
	localPlayerID := g.localPlayerID

	// Create a snapshot of players to avoid holding lock during draw
	playerSnapshot := make([]*types.Player, 0, len(g.players))
	for _, player := range g.players {
		// Create copy of player data
		playerCopy := *player
		playerSnapshot = append(playerSnapshot, &playerCopy)
	}
	g.mutex.RUnlock()

	// Draw all players without holding any locks
	for _, player := range playerSnapshot {
		g.drawPlayer(screen, player)
	}

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
func (g *GameClient) drawPlayer(screen *ebiten.Image, player *types.Player) {
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

	// Health (green)
	ebitenutil.DrawRect(screen, player.X-barWidth/2, player.Y+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
}

// drawEnemy renders a single enemy on the screen
func (g *GameClient) drawEnemy(screen *ebiten.Image, enemy *types.Enemy) {
	// Simple colored rectangle for now
	enemyColor := color.RGBA{0xff, 0xff, 0xff, 0xff} // white for now

	// Draw enemy as a 20x20 rectangle
	ebitenutil.DrawRect(screen, enemy.X-10, enemy.Y-10, 20, 20, enemyColor)

	// Draw enemy name
	ebitenutil.DebugPrintAt(screen, enemy.Name, int(enemy.X-20), int(enemy.Y-25))

	// Draw health bar
	barWidth := 30.0
	barHeight := 4.0
	healthPercent := float64(enemy.Health) / float64(enemy.MaxHealth)

	// Health (green)
	ebitenutil.DrawRect(screen, enemy.X-barWidth/2, enemy.Y+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
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
