package game

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	_ "image/png"
	"log"
	"math"
	"sync"
	"time"

	"github.com/CollinEMac/tarnation/internal/types"
	"github.com/CollinEMac/tarnation/internal/assets"
	"github.com/gorilla/websocket"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/inpututil"
	"github.com/hajimehoshi/ebiten/v2/text/v2"
	"golang.org/x/image/font/gofont/goregular"
)

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
	
	cameraX          float64
	cameraY          float64
	screenWidth      int
	screenHeight     int
	
	warriorSprite       *ebiten.Image
	dirtFloorSprite     *ebiten.Image
	criticalStrikeSprite *ebiten.Image
	
	fontFace            text.Face
}

func NewGameClient() *GameClient {
	client := &GameClient{
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
	
	client.loadWarriorSprite()
	client.loadDirtFloorSprite()
	client.loadCriticalStrikeSprite()
	
	client.loadFont()
	
	return client
}

func (g *GameClient) loadWarriorSprite() {
	img, _, err := image.Decode(bytes.NewReader(assets.WarriorPNG))
	if err != nil {
		log.Printf("Failed to load warrior sprite: %v", err)
		return
	}
	g.warriorSprite = ebiten.NewImageFromImage(img)
}

func (g *GameClient) loadDirtFloorSprite() {
	img, _, err := image.Decode(bytes.NewReader(assets.DirtFloorPNG))
	if err != nil {
		log.Printf("Failed to load dirt floor sprite: %v", err)
		return
	}
	g.dirtFloorSprite = ebiten.NewImageFromImage(img)
}

func (g *GameClient) loadCriticalStrikeSprite() {
	img, _, err := image.Decode(bytes.NewReader(assets.CriticalStrikePNG))
	if err != nil {
		log.Printf("Failed to load critical strike sprite: %v", err)
		return
	}
	g.criticalStrikeSprite = ebiten.NewImageFromImage(img)
}

func (g *GameClient) loadFont() {
	source, err := text.NewGoTextFaceSource(bytes.NewReader(goregular.TTF))
	if err != nil {
		log.Printf("Failed to load font: %v", err)
		return
	}
	g.fontFace = &text.GoTextFace{
		Source: source,
		Size:   12,
	}
}

func (g *GameClient) ConnectToServer(url string) error {
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server: %w", err)
	}

	g.conn = conn
	g.connected = true

	go g.handleMessages()

	g.addMessage("Connected to server!")
	return nil
}

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

func (g *GameClient) Update() error {
	g.mutex.RLock()
	connected := g.connected
	g.mutex.RUnlock()

	if !connected {
		return nil
	}

	g.handleInput()
	
	g.updateCamera()
	
	return nil
}

func (g *GameClient) Cleanup() {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	if g.connected && g.conn != nil {
		g.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		g.conn.Close()
		g.connected = false
	}
}

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

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonLeft) {
		mouseX, mouseY := ebiten.CursorPosition()
		worldX := float64(mouseX) + g.cameraX
		worldY := float64(mouseY) + g.cameraY
		
		if playerID := g.getPlayerAt(worldX, worldY); playerID != "" {
			g.selectedEntityID = playerID
			g.selectedEntityType = "player"
		} else if enemyID := g.getEnemyAt(worldX, worldY); enemyID != "" {
			g.selectedEntityID = enemyID
			g.selectedEntityType = "enemy"
		} else {
			g.selectedEntityID = ""
			g.selectedEntityType = ""
		}
	}

	if inpututil.IsMouseButtonJustPressed(ebiten.MouseButtonRight) {
		mouseX, mouseY := ebiten.CursorPosition()
		worldX := float64(mouseX) + g.cameraX
		worldY := float64(mouseY) + g.cameraY
		
		if enemyID := g.getEnemyAt(worldX, worldY); enemyID != "" {
			g.targetEnemyID = enemyID
			g.selectedEntityID = enemyID
			g.selectedEntityType = "enemy"
		} else {
			g.targetEnemyID = ""
		}
	}

	if inpututil.IsKeyJustPressed(ebiten.KeySpace) {
		g.sendMessage(types.MsgPlayerAction, map[string]string{
			"action": "basic_attack",
		})
	}

	if inpututil.IsKeyJustPressed(ebiten.Key1) {
		if g.targetEnemyID != "" && g.isLocalPlayerWarrior() {
			g.sendMessage(types.MsgPlayerAction, map[string]interface{}{
				"action": "critical_strike",
				"target": g.targetEnemyID,
			})
		}
	}

	if g.targetEnemyID != "" {
		g.mutex.RLock()
		targetEnemy, enemyExists := g.enemies[g.targetEnemyID]
		g.mutex.RUnlock()

		if !enemyExists {
			g.targetEnemyID = ""
		} else {
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
				moveSpeed := 3.0
				if distance > 0 {
					newX = localPlayer.X + (dx/distance)*moveSpeed
					newY = localPlayer.Y + (dy/distance)*moveSpeed
					moved = true
				}
			} else {
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

	if moved {
		g.mutex.RLock()
		walls := g.room.Walls
		g.mutex.RUnlock()
		
		validX, validY := g.checkWallCollisionWithSliding(localPlayer.X, localPlayer.Y, newX, newY, walls)
		
		if validX != localPlayer.X || validY != localPlayer.Y {
			g.mutex.Lock()
			localPlayer.X = validX
			localPlayer.Y = validY
			g.mutex.Unlock()

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

func (g *GameClient) updateCamera() {
	g.mutex.RLock()
	defer g.mutex.RUnlock()
	
	localPlayer, exists := g.players[g.localPlayerID]
	if !exists {
		return
	}
	
	// Camera deadzone (like classic Zelda - player can move within this area without camera moving)
	deadzoneWidth := float64(g.screenWidth) / 4   // 200 pixels
	deadzoneHeight := float64(g.screenHeight) / 4 // 150 pixels
	
	playerScreenX := localPlayer.X - g.cameraX
	playerScreenY := localPlayer.Y - g.cameraY
	
	deadzoneLeft := float64(g.screenWidth)/2 - deadzoneWidth/2
	deadzoneRight := float64(g.screenWidth)/2 + deadzoneWidth/2
	deadzoneTop := float64(g.screenHeight)/2 - deadzoneHeight/2
	deadzoneBottom := float64(g.screenHeight)/2 + deadzoneHeight/2
	
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

func (g *GameClient) Draw(screen *ebiten.Image) {
	screen.Fill(color.RGBA{0x20, 0x20, 0x20, 0xff})

	g.drawFloor(screen)
	g.drawWalls(screen)

	g.mutex.RLock()

	// Create a snapshot of players to avoid holding lock during draw
	enemySnapshot := make([]*types.Enemy, 0, len(g.enemies))
	for _, enemy := range g.enemies {
		// Create copy of enemy data
		enemyCopy := *enemy
		enemySnapshot = append(enemySnapshot, &enemyCopy)
	}
	g.mutex.RUnlock()

	for _, enemy := range enemySnapshot {
		g.drawEnemy(screen, enemy)
	}

	g.mutex.RLock()
	connected := g.connected
	playerCount := len(g.players)
	localPlayerID := g.localPlayerID

	playerSnapshot := make([]*types.Player, 0, len(g.players))
	for _, player := range g.players {
		// Create copy of player data
		playerCopy := *player
		playerSnapshot = append(playerSnapshot, &playerCopy)
	}
	g.mutex.RUnlock()

	for _, player := range playerSnapshot {
		g.drawPlayer(screen, player)
	}

	g.drawUI(screen)

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

func (g *GameClient) drawPlayer(screen *ebiten.Image, player *types.Player) {
	g.mutex.RLock()
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()
	
	screenX := player.X - cameraX
	screenY := player.Y - cameraY
	
	if screenX >= -20 && screenX <= float64(g.screenWidth)+20 &&
	   screenY >= -20 && screenY <= float64(g.screenHeight)+20 {
		
		if g.warriorSprite != nil {
			op := &ebiten.DrawImageOptions{}
			op.GeoM.Translate(screenX-16, screenY-16)
					
			screen.DrawImage(g.warriorSprite, op)
		} else {
			playerColor := color.RGBA{0x80, 0x80, 0xff, 0xff} // Blue for other players
			if player.ID == g.localPlayerID {
				playerColor = color.RGBA{0xff, 0x80, 0x80, 0xff} // Red for local player
			}
			ebitenutil.DrawRect(screen, screenX-10, screenY-10, 20, 20, playerColor)
		}

		opts := &text.DrawOptions{}
		opts.GeoM.Translate(screenX-20, screenY-25)
		text.Draw(screen, player.Name, g.fontFace, opts)

		barWidth := 30.0
		barHeight := 4.0
		healthPercent := float64(player.Health) / float64(player.MaxHealth)

		ebitenutil.DrawRect(screen, screenX-barWidth/2, screenY+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
	}
}

func (g *GameClient) drawEnemy(screen *ebiten.Image, enemy *types.Enemy) {
	g.mutex.RLock()
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()
	
	screenX := enemy.X - cameraX
	screenY := enemy.Y - cameraY
	
	if screenX >= -20 && screenX <= float64(g.screenWidth)+20 &&
	   screenY >= -20 && screenY <= float64(g.screenHeight)+20 {
		
		enemyColor := color.RGBA{0xff, 0xff, 0xff, 0xff}

		ebitenutil.DrawRect(screen, screenX-10, screenY-10, 20, 20, enemyColor)

		opts := &text.DrawOptions{}
		opts.GeoM.Translate(screenX-20, screenY-25)
		text.Draw(screen, enemy.Name, g.fontFace, opts)

		barWidth := 30.0
		barHeight := 4.0
		healthPercent := float64(enemy.Health) / float64(enemy.MaxHealth)

		ebitenutil.DrawRect(screen, screenX-barWidth/2, screenY+15, barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})
	}
}

func (g *GameClient) drawUI(screen *ebiten.Image) {
	status := "Disconnected"
	if g.connected {
		status = "Connected"
	}
	ebitenutil.DebugPrint(screen, fmt.Sprintf("Status: %s | Players: %d", status, len(g.players)))

	opts := &text.DrawOptions{}
	opts.GeoM.Translate(10, 30)
	text.Draw(screen, "Controls: WASD/Arrows to move, Space for action, Left click to select", g.fontFace, opts)

	g.drawNameplate(screen)
	g.drawPlayerResources(screen)
	g.drawActionBar(screen)
}

func (g *GameClient) drawPlayerResources(screen *ebiten.Image) {
	g.mutex.RLock()
	localPlayer, exists := g.players[g.localPlayerID]
	g.mutex.RUnlock()
	
	if !exists || localPlayer == nil {
		return
	}
	
	barWidth := 200.0
	barHeight := 20.0
	barSpacing := 4.0
	
	// Center the bar group with action bar center
	actionBarY := float64(g.screenHeight) - 56.0 - 10.0
	actionBarCenterY := actionBarY + 28.0 // Center of action bar
	
	totalBarsHeight := barHeight + barSpacing + barHeight
	
	barX := 20.0
	barY := actionBarCenterY - (totalBarsHeight / 2.0)
	
	healthPercent := float64(localPlayer.Health) / float64(localPlayer.MaxHealth)
	
	ebitenutil.DrawRect(screen, barX, barY, barWidth, barHeight, color.RGBA{0x40, 0x00, 0x00, 0xFF})
	ebitenutil.DrawRect(screen, barX, barY, barWidth*healthPercent, barHeight, color.RGBA{0xFF, 0x00, 0x00, 0xFF})
	ebitenutil.DrawRect(screen, barX-1, barY-1, barWidth+2, 1, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX-1, barY+barHeight, barWidth+2, 1, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX-1, barY-1, 1, barHeight+2, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX+barWidth, barY-1, 1, barHeight+2, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	
	opts := &text.DrawOptions{}
	opts.GeoM.Translate(barX, barY-15)
	text.Draw(screen, fmt.Sprintf("Health: %d/%d", localPlayer.Health, localPlayer.MaxHealth), g.fontFace, opts)
	
	resourceY := barY + barHeight + barSpacing
	resourcePercent := float64(localPlayer.Mana) / float64(localPlayer.MaxMana)
	
	var resourceLabel string
	var resourceBgColor, resourceFgColor color.RGBA
	
	if localPlayer.Class == "warrior" {
		resourceLabel = "Rage"
		resourceBgColor = color.RGBA{0x40, 0x20, 0x00, 0xFF} // Dark orange
		resourceFgColor = color.RGBA{0xFF, 0x80, 0x00, 0xFF} // Orange
	} else {
		resourceLabel = "Mana"
		resourceBgColor = color.RGBA{0x00, 0x20, 0x40, 0xFF} // Dark blue
		resourceFgColor = color.RGBA{0x00, 0x80, 0xFF, 0xFF} // Blue
	}
	
	ebitenutil.DrawRect(screen, barX, resourceY, barWidth, barHeight, resourceBgColor)
	ebitenutil.DrawRect(screen, barX, resourceY, barWidth*resourcePercent, barHeight, resourceFgColor)
	ebitenutil.DrawRect(screen, barX-1, resourceY-1, barWidth+2, 1, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX-1, resourceY+barHeight, barWidth+2, 1, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX-1, resourceY-1, 1, barHeight+2, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	ebitenutil.DrawRect(screen, barX+barWidth, resourceY-1, 1, barHeight+2, color.RGBA{0xFF, 0xFF, 0xFF, 0xFF})
	
	resourceOpts := &text.DrawOptions{}
	resourceOpts.GeoM.Translate(barX, resourceY-15)
	text.Draw(screen, fmt.Sprintf("%s: %d/%d", resourceLabel, localPlayer.Mana, localPlayer.MaxMana), g.fontFace, resourceOpts)
}

func (g *GameClient) drawActionBar(screen *ebiten.Image) {
	slotCount := 8
	slotSize := 40
	slotSpacing := 4
	barWidth := slotCount*slotSize + (slotCount-1)*slotSpacing + 16 // +16 for padding
	barHeight := slotSize + 16 // +16 for padding
	
	barX := (g.screenWidth - barWidth) / 2
	barY := g.screenHeight - barHeight - 10 // 10 pixels from bottom
	
	barBgColor := color.RGBA{0x20, 0x20, 0x20, 0xE0}
	barBorderColor := color.RGBA{0x60, 0x60, 0x60, 0xFF}
	
	ebitenutil.DrawRect(screen, float64(barX), float64(barY), float64(barWidth), float64(barHeight), barBgColor)
	
	ebitenutil.DrawRect(screen, float64(barX), float64(barY), float64(barWidth), 2, barBorderColor)
	ebitenutil.DrawRect(screen, float64(barX), float64(barY+barHeight-2), float64(barWidth), 2, barBorderColor)
	ebitenutil.DrawRect(screen, float64(barX), float64(barY), 2, float64(barHeight), barBorderColor)
	ebitenutil.DrawRect(screen, float64(barX+barWidth-2), float64(barY), 2, float64(barHeight), barBorderColor)
	
	slotBgColor := color.RGBA{0x40, 0x40, 0x40, 0xFF}
	slotBorderColor := color.RGBA{0x80, 0x80, 0x80, 0xFF}
	
	for i := 0; i < slotCount; i++ {
		slotX := barX + 8 + i*(slotSize+slotSpacing) // 8 for padding
		slotY := barY + 8 // 8 for padding
		
		ebitenutil.DrawRect(screen, float64(slotX), float64(slotY), float64(slotSize), float64(slotSize), slotBgColor)
		
		ebitenutil.DrawRect(screen, float64(slotX), float64(slotY), float64(slotSize), 1, slotBorderColor)
		ebitenutil.DrawRect(screen, float64(slotX), float64(slotY+slotSize-1), float64(slotSize), 1, slotBorderColor)
		ebitenutil.DrawRect(screen, float64(slotX), float64(slotY), 1, float64(slotSize), slotBorderColor)
		ebitenutil.DrawRect(screen, float64(slotX+slotSize-1), float64(slotY), 1, float64(slotSize), slotBorderColor)
		
		if i == 0 && g.criticalStrikeSprite != nil && g.isLocalPlayerWarrior() {
			op := &ebiten.DrawImageOptions{}
			
			iconSize := float64(slotSize - 4)
			spriteWidth, spriteHeight := g.criticalStrikeSprite.Bounds().Dx(), g.criticalStrikeSprite.Bounds().Dy()
			scaleX := iconSize / float64(spriteWidth)
			scaleY := iconSize / float64(spriteHeight)
			scale := min(scaleX, scaleY)
			
			op.GeoM.Scale(scale, scale)
			
			scaledWidth := float64(spriteWidth) * scale
			scaledHeight := float64(spriteHeight) * scale
			offsetX := (float64(slotSize) - scaledWidth) / 2
			offsetY := (float64(slotSize) - scaledHeight) / 2
			
			op.GeoM.Translate(float64(slotX)+offsetX, float64(slotY)+offsetY)
			screen.DrawImage(g.criticalStrikeSprite, op)
		} else {
			opts := &text.DrawOptions{}
			opts.GeoM.Translate(float64(slotX+slotSize/2-3), float64(slotY+slotSize/2-4))
			text.Draw(screen, fmt.Sprintf("%d", i+1), g.fontFace, opts)
		}
	}
}

func (g *GameClient) isLocalPlayerWarrior() bool {
	g.mutex.RLock()
	defer g.mutex.RUnlock()
	
	localPlayer, exists := g.players[g.localPlayerID]
	if !exists {
		return false
	}
	
	return localPlayer.Class == "warrior"
}

func (g *GameClient) drawNameplate(screen *ebiten.Image) {
	g.mutex.RLock()
	selectedID := g.selectedEntityID
	selectedType := g.selectedEntityType

	if selectedID == "" || selectedType == "" {
		g.mutex.RUnlock()
		return
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
		g.selectedEntityID = ""
		g.selectedEntityType = ""
		g.mutex.RUnlock()
		return
	}
	g.mutex.RUnlock()

	nameplateX := 600
	nameplateY := 10
	nameplateWidth := 180
	nameplateHeight := 80

	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), float64(nameplateWidth), float64(nameplateHeight), color.RGBA{0x00, 0x00, 0x00, 0x80})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), float64(nameplateWidth), 2, color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY+nameplateHeight-2), float64(nameplateWidth), 2, color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX), float64(nameplateY), 2, float64(nameplateHeight), color.RGBA{0xff, 0xff, 0xff, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX+nameplateWidth-2), float64(nameplateY), 2, float64(nameplateHeight), color.RGBA{0xff, 0xff, 0xff, 0xff})

	opts := &text.DrawOptions{}
	opts.GeoM.Translate(float64(nameplateX+5), float64(nameplateY+5))
	text.Draw(screen, fmt.Sprintf("Name: %s", name), g.fontFace, opts)
	opts = &text.DrawOptions{}
	opts.GeoM.Translate(float64(nameplateX+5), float64(nameplateY+20))
	text.Draw(screen, fmt.Sprintf("Health: %d/%d", health, maxHealth), g.fontFace, opts)
	opts = &text.DrawOptions{}
	opts.GeoM.Translate(float64(nameplateX+5), float64(nameplateY+35))
	text.Draw(screen, fmt.Sprintf("Mana: %d/%d", mana, maxMana), g.fontFace, opts)

	barWidth := float64(nameplateWidth - 10)
	barHeight := 8.0
	healthPercent := float64(health) / float64(maxHealth)
	
	ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+50), barWidth, barHeight, color.RGBA{0x80, 0x00, 0x00, 0xff})
	ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+50), barWidth*healthPercent, barHeight, color.RGBA{0x00, 0xff, 0x00, 0xff})

	manaPercent := float64(mana) / float64(maxMana)
	if maxMana > 0 {
		ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+62), barWidth, barHeight, color.RGBA{0x00, 0x00, 0x80, 0xff})
		ebitenutil.DrawRect(screen, float64(nameplateX+5), float64(nameplateY+62), barWidth*manaPercent, barHeight, color.RGBA{0x00, 0x80, 0xff, 0xff})
	}
}

func (g *GameClient) checkWallCollision(x, y float64, walls []types.Wall) bool {
	entitySize := 10.0
	
	for _, wall := range walls {
		if x-entitySize < wall.X+wall.Width &&
			x+entitySize > wall.X &&
			y-entitySize < wall.Y+wall.Height &&
			y+entitySize > wall.Y {
			return true
		}
	}
	return false
}

func (g *GameClient) checkWallCollisionWithSliding(oldX, oldY, newX, newY float64, walls []types.Wall) (float64, float64) {
	if !g.checkWallCollision(newX, newY, walls) {
		return newX, newY
	}
	
	if !g.checkWallCollision(newX, oldY, walls) {
		return newX, oldY
	}
	
	if !g.checkWallCollision(oldX, newY, walls) {
		return oldX, newY
	}
	
	return oldX, oldY
}

func (g *GameClient) drawFloor(screen *ebiten.Image) {
	if g.dirtFloorSprite == nil {
		return
	}
	
	g.mutex.RLock()
	walls := g.room.Walls
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()
	
	if len(walls) == 0 {
		return
	}
	
	minX, minY := walls[0].X, walls[0].Y
	maxX, maxY := walls[0].X+walls[0].Width, walls[0].Y+walls[0].Height
	
	for _, wall := range walls {
		if wall.X < minX {
			minX = wall.X
		}
		if wall.Y < minY {
			minY = wall.Y
		}
		if wall.X+wall.Width > maxX {
			maxX = wall.X + wall.Width
		}
		if wall.Y+wall.Height > maxY {
			maxY = wall.Y + wall.Height
		}
	}
	
	spriteWidth, spriteHeight := g.dirtFloorSprite.Bounds().Dx(), g.dirtFloorSprite.Bounds().Dy()
	
	startX := int((cameraX / float64(spriteWidth)) - 1) * spriteWidth
	startY := int((cameraY / float64(spriteHeight)) - 1) * spriteHeight
	endX := int(cameraX) + g.screenWidth + spriteWidth
	endY := int(cameraY) + g.screenHeight + spriteHeight
	
	for x := startX; x < endX; x += spriteWidth {
		for y := startY; y < endY; y += spriteHeight {
			if float64(x) > minX && float64(y) > minY && 
			   float64(x) < maxX-float64(spriteWidth) && float64(y) < maxY-float64(spriteHeight) {
				
				screenX := float64(x) - cameraX
				screenY := float64(y) - cameraY
				
				if screenX > -float64(spriteWidth) && screenX < float64(g.screenWidth) &&
				   screenY > -float64(spriteHeight) && screenY < float64(g.screenHeight) {
					
					op := &ebiten.DrawImageOptions{}
					op.GeoM.Translate(screenX, screenY)
					screen.DrawImage(g.dirtFloorSprite, op)
				}
			}
		}
	}
}

func (g *GameClient) drawWalls(screen *ebiten.Image) {
	g.mutex.RLock()
	walls := g.room.Walls
	cameraX := g.cameraX
	cameraY := g.cameraY
	g.mutex.RUnlock()

	wallColor := color.RGBA{0x80, 0x80, 0x80, 0xff}
	for _, wall := range walls {
		screenX := wall.X - cameraX
		screenY := wall.Y - cameraY
		
		if screenX+wall.Width >= 0 && screenX <= float64(g.screenWidth) &&
		   screenY+wall.Height >= 0 && screenY <= float64(g.screenHeight) {
			ebitenutil.DrawRect(screen, screenX, screenY, wall.Width, wall.Height, wallColor)
		}
	}
}

func (g *GameClient) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return 800, 600
}

func (g *GameClient) getEnemyAt(x, y float64) string {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	for enemyID, enemy := range g.enemies {
		if x >= enemy.X-10 && x <= enemy.X+10 &&
			y >= enemy.Y-10 && y <= enemy.Y+10 {
			return enemyID
		}
	}
	return ""
}

func (g *GameClient) getPlayerAt(x, y float64) string {
	g.mutex.RLock()
	defer g.mutex.RUnlock()

	for playerID, player := range g.players {
		if x >= player.X-10 && x <= player.X+10 &&
			y >= player.Y-10 && y <= player.Y+10 {
			return playerID
		}
	}
	return ""
}

func (g *GameClient) addMessage(msg string) {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	g.messages = append([]string{fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), msg)}, g.messages...)

	if len(g.messages) > 10 {
		g.messages = g.messages[:10]
	}
}
