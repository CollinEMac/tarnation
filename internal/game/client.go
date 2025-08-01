package game

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"math"
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
	conn             *websocket.Conn
	players          map[string]*types.Player
	enemies          map[string]*types.Enemy
	room             types.Room
	localPlayerID    string
	targetEnemyID    string    // ID of currently targeted enemy
	selectedEntityID string    // ID of currently selected entity (for nameplate)
	selectedEntityType string  // Type of selected entity ("player" or "enemy")
	lastAttackTime   time.Time // For attack timing
	mutex            sync.RWMutex
	connected        bool
	lastMoveTime     time.Time
	moveThrottle     time.Duration
	messages         []string // For displaying debug info
	shouldClose      bool     // Flag to indicate clean shutdown
	
	// Camera system
	cameraX          float64   // Camera position X
	cameraY          float64   // Camera position Y
	screenWidth      int       // Screen dimensions
	screenHeight     int
}

// NewGameClient creates a new game client instance
func NewGameClient() *GameClient {
	return &GameClient{
		players:      make(map[string]*types.Player),
		enemies:      make(map[string]*types.Enemy),
		moveThrottle: 16 * time.Millisecond, // Limit movement updates to ~60/sec to match render loop
		messages:     make([]string, 0),
		shouldClose:  false,
		screenWidth:  800,
		screenHeight: 600,
		cameraX:      0,
		cameraY:      0,
	}
}

// ConnectToServer establishes WebSocket connection to game server
func (g *GameClient) ConnectToServer(url string) error {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	g.conn = conn
	g.connected = true

	// Start message handling goroutine
	go g.handleMessages()

	g.addMessage("Connected to server!")
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

	case types.MsgPlayerUpdate:
		var player types.Player
		if err := json.Unmarshal(msg.Data, &player); err != nil {
			log.Printf("Error unmarshaling player update: %v", err)
			return
		}

		g.mutex.Lock()
		if existingPlayer, exists := g.players[msg.PlayerID]; exists {
			// Update player stats but keep position
			existingPlayer.Health = player.Health
			existingPlayer.MaxHealth = player.MaxHealth
			existingPlayer.Mana = player.Mana
			existingPlayer.MaxMana = player.MaxMana
		}
		g.mutex.Unlock()

	case types.MsgPlayerAction:

	case types.MsgEnemySpawn:
		var enemy types.Enemy
		if err := json.Unmarshal(msg.Data, &enemy); err != nil {
			log.Printf("Error unmarshaling enemy spawn: %v", err)
			return
		}

		g.mutex.Lock()
		g.enemies[enemy.ID] = &enemy
		g.mutex.Unlock()


	case types.MsgEnemyUpdate:
		// Try to parse as death message first
		var deathData struct {
			ID   string `json:"id"`
			Dead bool   `json:"dead,omitempty"`
		}

		if err := json.Unmarshal(msg.Data, &deathData); err == nil && deathData.Dead {
			// Enemy is dead - remove from game
			g.mutex.Lock()
			var enemyName string
			if enemy, exists := g.enemies[deathData.ID]; exists {
				enemyName = enemy.Name
				delete(g.enemies, deathData.ID)

				// Clear target if this was our target
				if g.targetEnemyID == deathData.ID {
					g.targetEnemyID = ""
				}
			}
			g.mutex.Unlock()

			// Add message after releasing the lock to avoid deadlock
			if enemyName != "" {
				g.addMessage(fmt.Sprintf("Enemy %s has been defeated!", enemyName))
			}
		} else {
			// Parse as full enemy update
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
				existingEnemy.MaxHealth = enemy.MaxHealth
			}
			g.mutex.Unlock()
		}

	case types.MsgRoomData:
		var room types.Room
		if err := json.Unmarshal(msg.Data, &room); err != nil {
			log.Printf("Error unmarshaling room data: %v", err)
			return
		}

		g.mutex.Lock()
		g.room = room
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
	
	// Update camera to follow local player
	g.updateCamera()
	
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

	// Handle player selection (left click)
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mouseX, mouseY := ebiten.CursorPosition()
		// Convert screen coordinates to world coordinates
		worldX := float64(mouseX) + g.cameraX
		worldY := float64(mouseY) + g.cameraY
		
		// Check for player first (players have priority over enemies for selection)
		if playerID := g.getPlayerAt(worldX, worldY); playerID != "" {
			g.selectedEntityID = playerID
			g.selectedEntityType = "player"
		} else if enemyID := g.getEnemyAt(worldX, worldY); enemyID != "" {
			g.selectedEntityID = enemyID
			g.selectedEntityType = "enemy"
		} else {
			// Clear selection if clicking empty space
			g.selectedEntityID = ""
			g.selectedEntityType = ""
		}
	}

	// Handle player targeting (right click)
	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight) {
		mouseX, mouseY := ebiten.CursorPosition()
		// Convert screen coordinates to world coordinates
		worldX := float64(mouseX) + g.cameraX
		worldY := float64(mouseY) + g.cameraY
		
		if enemyID := g.getEnemyAt(worldX, worldY); enemyID != "" {
			g.targetEnemyID = enemyID
			// Also select the enemy for nameplate display
			g.selectedEntityID = enemyID
			g.selectedEntityType = "enemy"
		} else {
			g.targetEnemyID = "" // Clear target if clicking empty space
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		g.sendMessage(types.MsgPlayerAction, map[string]string{
			"action": "basic_attack",
		})
	}

	// Handle auto-combat with targeted enemy
	if g.targetEnemyID != "" {
		g.mutex.RLock()
		targetEnemy, enemyExists := g.enemies[g.targetEnemyID]
		g.mutex.RUnlock()

		if !enemyExists {
			// Target is dead or doesn't exist, clear target
			g.targetEnemyID = ""
		} else {
			// Calculate distance to target
			dx := targetEnemy.X - localPlayer.X
			dy := targetEnemy.Y - localPlayer.Y
			distance := math.Sqrt(dx*dx + dy*dy)

			weaponRange := 30.0        // Default range
			weaponDelay := time.Second // Default weapon speed
			if localPlayer.Weapon != nil {
				weaponRange = float64(localPlayer.Weapon.Range * 25) // Scale range
				weaponDelay = localPlayer.Weapon.Delay
			}

			if distance > weaponRange {
				// Move toward target
				moveSpeed := 3.0
				if distance > 0 {
					// Normalize direction and move
					newX = localPlayer.X + (dx/distance)*moveSpeed
					newY = localPlayer.Y + (dy/distance)*moveSpeed
					moved = true
				}
			} else {
				// In range - attack if enough time has passed
				if time.Since(g.lastAttackTime) > weaponDelay {
					g.sendMessage(types.MsgPlayerAction, map[string]interface{}{
						"action": "attack",
						"target": g.targetEnemyID,
					})
					g.lastAttackTime = time.Now()
				}
			}
		}
	}

	// Send movement update if player moved
	if moved {
		// Use sliding collision detection
		g.mutex.RLock()
		walls := g.room.Walls
		g.mutex.RUnlock()
		
		validX, validY := g.checkWallCollisionWithSliding(localPlayer.X, localPlayer.Y, newX, newY, walls)
		
		// Only update if position actually changed
		if validX != localPlayer.X || validY != localPlayer.Y {
			// Update local position immediately for responsive feel
			g.mutex.Lock()
			localPlayer.X = validX
			localPlayer.Y = validY
			g.mutex.Unlock()

			// Send update to server
			moveData := map[string]float64{
				"x": validX,
				"y": validY,
			}

			if err := g.sendMessage(types.MsgPlayerMove, moveData); err != nil {
				log.Printf("Error sending move: %v", err)
			}

			g.lastMoveTime = time.Now()
		}
	}
}

// updateCamera updates the camera position to follow the local player
func (g *GameClient) updateCamera() {
	g.mutex.RLock()
	defer g.mutex.RUnlock()
	
	// Get local player
	localPlayer, exists := g.players[g.localPlayerID]
	if !exists {
		return
	}
	
	// Camera deadzone (like classic Zelda - player can move within this area without camera moving)
	deadzoneWidth := float64(g.screenWidth) / 4   // 200 pixels
	deadzoneHeight := float64(g.screenHeight) / 4 // 150 pixels
	
	// Calculate player position relative to current camera
	playerScreenX := localPlayer.X - g.cameraX
	playerScreenY := localPlayer.Y - g.cameraY
	
	// Calculate deadzone boundaries
	deadzoneLeft := float64(g.screenWidth)/2 - deadzoneWidth/2
	deadzoneRight := float64(g.screenWidth)/2 + deadzoneWidth/2
	deadzoneTop := float64(g.screenHeight)/2 - deadzoneHeight/2
	deadzoneBottom := float64(g.screenHeight)/2 + deadzoneHeight/2
	
	// Move camera if player is outside deadzone
	if playerScreenX < deadzoneLeft {
		g.cameraX = localPlayer.X - deadzoneLeft
	} else if playerScreenX > deadzoneRight {
		g.cameraX = localPlayer.X - deadzoneRight
	}
	
	if playerScreenY < deadzoneTop {
		g.cameraY = localPlayer.Y - deadzoneTop
	} else if playerScreenY > deadzoneBottom {
		g.cameraY = localPlayer.Y - deadzoneBottom
	}
	
}

// Draw implements ebiten.Game interface
func (g *GameClient) Draw(screen *ebiten.Image) {
	// Clear screen with dark background
	screen.Fill(color.RGBA{0x20, 0x20, 0x20, 0xff})

	// Draw walls first (as background)
	g.drawWalls(screen)

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
		// ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Local player: %s", localPlayerID[:8]), 10, 70)
		// ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Camera: (%.0f, %.0f)", g.cameraX, g.cameraY), 10, 85)
	}
}

// drawPlayer renders a player on screen
func (g *GameClient) drawPlayer(screen *ebiten.Image, player *types.Player) {
	// Get camera position
	g.mutex.RLock()
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()
	
	// Calculate screen position
	screenX := player.X - cameraX
	screenY := player.Y - cameraY
	
	// Only draw if player is visible on screen
	if screenX >= -20 && screenX <= float64(g.screenWidth)+20 &&
	   screenY >= -20 && screenY <= float64(g.screenHeight)+20 {
		
		// Simple colored rectangle for now
		playerColor := color.RGBA{0x80, 0x80, 0xff, 0xff} // Blue for other players
		if player.ID == g.localPlayerID {
			playerColor = color.RGBA{0xff, 0x80, 0x80, 0xff} // Red for local player
		}

		// Draw player as a 20x20 rectangle
		ebitenutil.DrawRect(screen, screenX-10, screenY-10, 20, 20, playerColor)

		// Draw player name
		ebitenutil.DebugPrintAt(screen, player.Name, int(screenX-20), int(screenY-25))

		// Draw health bar
		barWidth := 30.0
		barHeight := 4.0
		healthPercent := float64(player.Health) / float64(player.MaxHealth)

		// Health (green)
		ebitenutil.DrawRect(screen, screenX-barWidth/2, screenY+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
	}
}

// drawEnemy renders a single enemy on the screen
func (g *GameClient) drawEnemy(screen *ebiten.Image, enemy *types.Enemy) {
	// Get camera position
	g.mutex.RLock()
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()
	
	// Calculate screen position
	screenX := enemy.X - cameraX
	screenY := enemy.Y - cameraY
	
	// Only draw if enemy is visible on screen
	if screenX >= -20 && screenX <= float64(g.screenWidth)+20 &&
	   screenY >= -20 && screenY <= float64(g.screenHeight)+20 {
		
		// Simple colored rectangle for now
		enemyColor := color.RGBA{0xff, 0xff, 0xff, 0xff} // white for now

		// Draw enemy as a 20x20 rectangle
		ebitenutil.DrawRect(screen, screenX-10, screenY-10, 20, 20, enemyColor)

		// Draw enemy name
		ebitenutil.DebugPrintAt(screen, enemy.Name, int(screenX-20), int(screenY-25))

		// Draw health bar
		barWidth := 30.0
		barHeight := 4.0
		healthPercent := float64(enemy.Health) / float64(enemy.MaxHealth)

		// Health (green)
		ebitenutil.DrawRect(screen, screenX-barWidth/2, screenY+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
	}
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
	ebitenutil.DebugPrintAt(screen, "Controls: WASD/Arrows to move, Space for action, Left click to select", 10, 30)

	// Draw nameplate for selected entity
	g.drawNameplate(screen)
}

// drawNameplate renders the nameplate for the currently selected entity
func (g *GameClient) drawNameplate(screen *ebiten.Image) {
	g.mutex.RLock()
	selectedID := g.selectedEntityID
	selectedType := g.selectedEntityType

	if selectedID == "" || selectedType == "" {
		g.mutex.RUnlock()
		return // No entity selected
	}

	var name string
	var health, maxHealth, mana, maxMana int
	var exists bool

	if selectedType == "player" {
		if player, ok := g.players[selectedID]; ok {
			name = player.Name
			health = player.Health
			maxHealth = player.MaxHealth
			mana = player.Mana
			maxMana = player.MaxMana
			exists = true
		}
	} else if selectedType == "enemy" {
		if enemy, ok := g.enemies[selectedID]; ok {
			name = enemy.Name
			health = enemy.Health
			maxHealth = enemy.MaxHealth
			mana = enemy.Mana
			maxMana = enemy.MaxMana
			exists = true
		}
	}

	if !exists {
		// Entity no longer exists, clear selection
		g.selectedEntityID = ""
		g.selectedEntityType = ""
		g.mutex.RUnlock()
		return
	}
	g.mutex.RUnlock()

	// Position nameplate in top-right area
	nameplateX := 600
	nameplateY := 10
	nameplateWidth := 180
	nameplateHeight := 80

	// Draw background
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), float64(nameplateWidth), float64(nameplateHeight), color.RGBA{0x00, 0x00, 0x00, 0x80})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), float64(nameplateWidth), 2, color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY+nameplateHeight-2), float64(nameplateWidth), 2, color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), 2, float64(nameplateHeight), color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX+nameplateWidth-2), float64(nameplateY), 2, float64(nameplateHeight), color.RGBA{0xff, 0xff, 0xff, 0xff})

	// Draw entity information
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Name: %s", name), nameplateX+5, nameplateY+5)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Health: %d/%d", health, maxHealth), nameplateX+5, nameplateY+20)
	ebitenutil.DebugPrintAt(screen, fmt.Sprintf("Mana: %d/%d", mana, maxMana), nameplateX+5, nameplateY+35)

	// Draw health bar
	barWidth := float64(nameplateWidth - 10)
	barHeight := 8.0
	healthPercent := float64(health) / float64(maxHealth)
	
	// Health bar background (red)
	ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+50), barWidth, barHeight, color.RGBA{0x80, 0x00, 0x00, 0xff})
	// Health bar foreground (green)
	ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+50), barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})

	// Draw mana bar
	manaPercent := float64(mana) / float64(maxMana)
	if maxMana > 0 {
		// Mana bar background (dark blue)
		ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+62), barWidth, barHeight, color.RGBA{0x00, 0x00, 0x80, 0xff})
		// Mana bar foreground (blue)
		ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+62), barWidth*manaPercent, barHeight, color.RGBA{0x00, 0x80, 0xff, 0xff})
	}
}

// checkWallCollision checks if a position would collide with any walls
func (g *GameClient) checkWallCollision(x, y float64, walls []types.Wall) bool {
	entitySize := 10.0 // Half the size of player/enemy (20x20 rectangle)
	
	for _, wall := range walls {
		// Check if entity bounds intersect with wall bounds
		if x-entitySize < wall.X+wall.Width &&
			x+entitySize > wall.X &&
			y-entitySize < wall.Y+wall.Height &&
			y+entitySize > wall.Y {
			return true
		}
	}
	return false
}

// checkWallCollisionWithSliding checks collision and returns valid position allowing sliding
func (g *GameClient) checkWallCollisionWithSliding(oldX, oldY, newX, newY float64, walls []types.Wall) (float64, float64) {
	// If new position doesn't collide, allow the move
	if !g.checkWallCollision(newX, newY, walls) {
		return newX, newY
	}
	
	// Try horizontal movement only (keep old Y)
	if !g.checkWallCollision(newX, oldY, walls) {
		return newX, oldY
	}
	
	// Try vertical movement only (keep old X)
	if !g.checkWallCollision(oldX, newY, walls) {
		return oldX, newY
	}
	
	// Can't move in any direction, stay at old position
	return oldX, oldY
}

// drawWalls renders the room walls
func (g *GameClient) drawWalls(screen *ebiten.Image) {
	g.mutex.RLock()
	walls := g.room.Walls
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()

	// Draw each wall as a gray rectangle, offset by camera position
	wallColor := color.RGBA{0x80, 0x80, 0x80, 0xff}
	for _, wall := range walls {
		screenX := wall.X - cameraX
		screenY := wall.Y - cameraY
		
		// Only draw walls that are visible on screen
		if screenX+wall.Width >= 0 && screenX <= float64(g.screenWidth) &&
		   screenY+wall.Height >= 0 && screenY <= float64(g.screenHeight) {
			ebitenutil.DrawRect(screen, screenX, screenY, wall.Width, wall.Height, wallColor)
		}
	}
}

// Layout implements ebiten.Game interface
func (g *GameClient) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return 800, 600
}

// getEnemyAt checks if there's an enemy at the given screen coordinates
func (g *GameClient) getEnemyAt(x, y float64) string {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	for enemyID, enemy := range g.enemies {
		// Check if click is within enemy bounds (20x20 rectangle)
		if x >= enemy.X-10 && x <= enemy.X+10 &&
			y >= enemy.Y-10 && y <= enemy.Y+10 {
			return enemyID
		}
	}
	return ""
}

// getPlayerAt checks if there's a player at the given screen coordinates
func (g *GameClient) getPlayerAt(x, y float64) string {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	for playerID, player := range g.players {
		// Check if click is within player bounds (20x20 rectangle)
		if x >= player.X-10 && x <= player.X+10 &&
			y >= player.Y-10 && y <= player.Y+10 {
			return playerID
		}
	}
	return ""
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
