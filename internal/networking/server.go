package networking

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/CollinEMac/tarnation/internal/game"
	"github.com/CollinEMac/tarnation/internal/types"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// GameServer manages all connected players and game instances
type GameServer struct {
	players   map[string]*types.Player
	enemies   map[string]*types.Enemy
	room      types.Room
	mutex     sync.RWMutex
	upgrader  websocket.Upgrader
	broadcast chan types.Message
}

// NewGameServer creates a new game server instance
func NewGameServer() *GameServer {
	server := &GameServer{
		players:   make(map[string]*types.Player),
		enemies:   make(map[string]*types.Enemy),
		room:      game.CreateDungeonRoom(),
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

	// Start the enemy AI goroutine
	go server.handleEnemyAI()

	// Start the rage decay goroutine
	go server.handleRageDecay()

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
	weaponID := uuid.New().String()
	player := &types.Player{
		ID:        playerID,
		Name:      "Player " + playerID[:8], // Default name
		X:         400,                      // Spawn in center of larger room
		Y:         300,
		Class:     "warrior", // Default class
		Health:    100,
		MaxHealth: 100,
		Mana:      0,   // Warriors start with 0 rage
		MaxMana:   100, // Maximum rage for warriors
		Conn:      conn,
		Weapon: &types.Weapon{
			ID:         weaponID,
			Name:       "Wooden Sword",
			Damage:     5,
			Range:      1,
			WeaponType: "sword",
			Delay:      time.Second,
		},
	}

	// Add player to server
	s.mutex.Lock()
	s.players[playerID] = player
	isFirstPlayer := len(s.players) == 1
	s.mutex.Unlock()

	log.Printf("Player %s connected", playerID)

	// Spawn initial enemies if this is the first player
	if isFirstPlayer {
		log.Println("First player joined, spawning initial enemies")
		go s.spawnInitialEnemies() // Run in separate goroutine to avoid race condition
	}

	// Send welcome message
	welcomeMsg := types.Message{
		Type:     types.MsgPlayerJoin,
		PlayerID: playerID,
		Data:     s.marshal(player),
	}

	player.ConnMutex.Lock()
	err = conn.WriteJSON(welcomeMsg)
	player.ConnMutex.Unlock()
	
	if err != nil {
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
			Data:     s.marshal(existingPlayer),
		}

		player.ConnMutex.Lock()
		err := conn.WriteJSON(existingPlayerMsg)
		player.ConnMutex.Unlock()
		
		if err != nil {
			log.Printf("Error sending existing player %s data to new player: %v", existingPlayerID, err)
		}
	}

	// Send information about all existing enemies to the new player (skip first player)
	if !isFirstPlayer {
		for enemyID, enemy := range s.enemies {
			enemyMsg := types.Message{
				Type: types.MsgEnemySpawn,
				Data: s.marshal(enemy),
			}

			player.ConnMutex.Lock()
			err := conn.WriteJSON(enemyMsg)
			player.ConnMutex.Unlock()
			
			if err != nil {
				log.Printf("Error sending existing enemy %s data to new player: %v", enemyID, err)
			}
		}
	}
	
	// Send room data to new player
	roomMsg := types.Message{
		Type: types.MsgRoomData,
		Data: s.marshal(s.room),
	}
	player.ConnMutex.Lock()
	err = conn.WriteJSON(roomMsg)
	player.ConnMutex.Unlock()
	
	if err != nil {
		log.Printf("Error sending room data to new player: %v", err)
	}
	
	s.mutex.RUnlock()

	// Broadcast player join to all other players
	s.broadcast <- types.Message{
		Type:     types.MsgPlayerJoin,
		PlayerID: playerID,
		Data:     s.marshal(player),
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

		// Update player position with sliding collision detection
		s.mutex.Lock()
		validX, validY := game.CheckWallCollisionWithSliding(player.X, player.Y, moveData.X, moveData.Y, s.room.Walls)
		player.X = validX
		player.Y = validY
		s.mutex.Unlock()

		// Broadcast position update
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerMove,
			PlayerID: player.ID,
			Data:     msg.Data,
		}

	case types.MsgPlayerAction:
		var actionData struct {
			Action string `json:"action"`
			Target string `json:"target,omitempty"`
		}

		if err := json.Unmarshal(msg.Data, &actionData); err != nil {
			log.Printf("Error unmarshaling action data: %v", err)
			return
		}

		if actionData.Action == "attack" && actionData.Target != "" {
			s.handleCombat(player, actionData.Target)
		} else if actionData.Action == "critical_strike" && actionData.Target != "" {
			s.handleCriticalStrike(player, actionData.Target)
		} else {
			// Handle other actions or broadcast generic actions
			log.Printf("Player %s used action: %s", player.ID, actionData.Action)
			s.broadcast <- types.Message{
				Type:     types.MsgPlayerAction,
				PlayerID: player.ID,
				Data:     msg.Data,
			}
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

			player.ConnMutex.Lock()
			err := player.Conn.WriteJSON(msg)
			player.ConnMutex.Unlock()
			
			if err != nil {
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

// spawnInitialEnemies creates the starting enemies for the game world
func (s *GameServer) spawnInitialEnemies() {
	for i := 0; i < 3; i++ {
		enemyID := uuid.New().String()
		enemy := &types.Enemy{
			ID:         enemyID,
			Name:       "Enemy " + enemyID[:8],
			X:          200 + float64(i*300), // Spawn enemies across larger room
			Y:          200 + float64(i*150),
			EnemyType:  "basic",
			Health:     50,
			MaxHealth:  50,
			TargetID:   "", // No target initially
			ThreatList: make(map[string]float64),
			Weapon: &types.Weapon{
				ID:         uuid.New().String(),
				Name:       "Claws",
				Damage:     3,
				Range:      1,
				WeaponType: "melee",
				Delay:      2 * time.Second,
			},
		}

		s.enemies[enemyID] = enemy
		log.Printf("Spawned enemy %s at (%.0f, %.0f)", enemy.Name, enemy.X, enemy.Y)

		// Broadcast enemy spawn to all players
		s.broadcast <- types.Message{
			Type: types.MsgEnemySpawn,
			Data: s.marshal(enemy),
		}
	}
}

// handleRageDecay manages rage decay for warriors over time
func (s *GameServer) handleRageDecay() {
	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	for range ticker.C {
		s.mutex.Lock()
		
		for _, player := range s.players {
			// Only process warriors with rage
			if player.Class == "warrior" && player.Mana > 0 {
				// Simple decay: lose 2 rage every tick (every 2 seconds)
				rageDecay := 2
				
				oldRage := player.Mana
				player.Mana -= rageDecay
				
				// Ensure rage doesn't go below 0
				if player.Mana < 0 {
					player.Mana = 0
				}
				
				// Only broadcast if rage actually changed
				if player.Mana != oldRage {
					// Broadcast rage update
					s.broadcast <- types.Message{
						Type:     types.MsgPlayerUpdate,
						PlayerID: player.ID,
						Data:     s.marshal(player),
					}
				}
			}
		}
		
		s.mutex.Unlock()
	}
}

// marshal converts any struct to JSON for network transmission
func (s *GameServer) marshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// handleCriticalStrike processes critical strike ability (warrior only)
func (s *GameServer) handleCriticalStrike(attacker *types.Player, targetEnemyID string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Check if player is a warrior
	if attacker.Class != "warrior" {
		log.Printf("Player %s (%s) attempted critical strike but is not a warrior", attacker.Name, attacker.Class)
		return
	}

	// Check if player has enough rage
	rageCost := 30 // Critical strike costs 30 rage
	if attacker.Mana < rageCost {
		log.Printf("Player %s attempted critical strike but lacks rage (%d/%d)", attacker.Name, attacker.Mana, rageCost)
		return
	}

	// Find the target enemy
	enemy, exists := s.enemies[targetEnemyID]
	if !exists {
		log.Printf("Critical Strike: Enemy %s not found", targetEnemyID)
		return
	}

	// Consume rage
	attacker.Mana -= rageCost

	// Calculate critical damage (2x normal damage + bonus)
	baseDamage := 1
	if attacker.Weapon != nil {
		baseDamage = attacker.Weapon.Damage
	}
	critDamage := (baseDamage * 2) + 3 // 2x damage + 3 bonus

	// Apply damage
	enemy.Health -= critDamage
	log.Printf("Player %s used Critical Strike on %s for %d damage (HP: %d/%d)",
		attacker.Name, enemy.Name, critDamage, enemy.Health, enemy.MaxHealth)

	// Add threat based on damage dealt
	enemy.ThreatList[attacker.ID] += float64(critDamage)
	
	// Update target based on highest threat
	s.updateEnemyTarget(enemy)

	// Broadcast player rage update (rage was consumed)
	s.broadcast <- types.Message{
		Type:     types.MsgPlayerUpdate,
		PlayerID: attacker.ID,
		Data:     s.marshal(attacker),
	}

	if enemy.Health <= 0 {
		// Enemy is dead - remove from game
		delete(s.enemies, targetEnemyID)
		log.Printf("Enemy %s has been defeated by %s's critical strike!", enemy.Name, attacker.Name)

		// Broadcast enemy death
		s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: s.marshal(map[string]interface{}{
				"id":   targetEnemyID,
				"dead": true,
			}),
		}
	} else {
		// Enemy still alive, broadcast health update
		s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: s.marshal(enemy),
		}
	}
}

// handleCombat processes player attacks on enemies
func (s *GameServer) handleCombat(attacker *types.Player, targetEnemyID string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	// Find the target enemy
	enemy, exists := s.enemies[targetEnemyID]
	if !exists {
		log.Printf("Combat: Enemy %s not found", targetEnemyID)
		return
	}

	// Calculate damage
	damage := 1 // Default damage
	if attacker.Weapon != nil {
		damage = attacker.Weapon.Damage
	}

	// Apply damage
	enemy.Health -= damage
	log.Printf("Player %s attacked %s for %d damage (HP: %d/%d)",
		attacker.Name, enemy.Name, damage, enemy.Health, enemy.MaxHealth)

	// Generate rage for attacking (warriors only)
	if attacker.Class == "warrior" {
		rageGain := 5 // Base rage gained per attack
		if attacker.Mana < attacker.MaxMana {
			attacker.Mana = min(attacker.MaxMana, attacker.Mana + rageGain)
			
			// Broadcast player rage update
			s.broadcast <- types.Message{
				Type:     types.MsgPlayerUpdate,
				PlayerID: attacker.ID,
				Data:     s.marshal(attacker),
			}
		}
	}

	// Add threat based on damage dealt
	enemy.ThreatList[attacker.ID] += float64(damage)
	
	// Update target based on highest threat
	s.updateEnemyTarget(enemy)

	if enemy.Health <= 0 {
		// Enemy is dead - remove from game
		delete(s.enemies, targetEnemyID)
		log.Printf("Enemy %s has been defeated", enemy.Name)

		// Broadcast enemy death
		deathData := map[string]interface{}{
			"id":   targetEnemyID,
			"dead": true,
		}
		deathJSON, _ := json.Marshal(deathData)
		s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: deathJSON,
		}
	} else {
		// Enemy still alive - broadcast health update
		s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: s.marshal(enemy),
		}
	}
}

// updateEnemyTarget selects the player with highest threat as the new target
func (s *GameServer) updateEnemyTarget(enemy *types.Enemy) {
	var highestThreat float64
	var newTargetID string
	
	// Find player with highest threat that still exists
	for playerID, threat := range enemy.ThreatList {
		if _, exists := s.players[playerID]; exists && threat > highestThreat {
			highestThreat = threat
			newTargetID = playerID
		}
	}
	
	// Update target if it changed
	if newTargetID != enemy.TargetID {
		oldTarget := enemy.TargetID
		enemy.TargetID = newTargetID
		
		if oldTarget != "" && newTargetID != "" {
			log.Printf("Enemy %s switched target from %s to %s (threat: %.1f)", 
				enemy.Name, oldTarget[:8], newTargetID[:8], highestThreat)
		} else if newTargetID != "" {
			log.Printf("Enemy %s now targeting %s (threat: %.1f)", 
				enemy.Name, newTargetID[:8], highestThreat)
		}
	}
}

// handleEnemyAI manages AI behavior for all enemies
func (s *GameServer) handleEnemyAI() {
	ticker := time.NewTicker(100 * time.Millisecond) // Update AI 10 times per second
	defer ticker.Stop()

	for range ticker.C {
		s.mutex.Lock()
		
		// Process each enemy
		for _, enemy := range s.enemies {
			s.processEnemyAI(enemy)
		}
		
		s.mutex.Unlock()
	}
}

// processEnemyAI handles AI logic for a single enemy
func (s *GameServer) processEnemyAI(enemy *types.Enemy) {
	// Clean up threat list - remove disconnected players
	for playerID := range enemy.ThreatList {
		if _, exists := s.players[playerID]; !exists {
			delete(enemy.ThreatList, playerID)
		}
	}
	
	// Add threat for all players within range
	s.addRangeThreat(enemy)
	
	// If enemy has no target, look for nearby players or highest threat
	if enemy.TargetID == "" {
		s.findNearbyTarget(enemy)
	} else {
		// Check if target still exists
		target, exists := s.players[enemy.TargetID]
		if !exists {
			enemy.TargetID = ""
			s.updateEnemyTarget(enemy) // Try to find new target from threat list
			return
		}

		// Calculate distance to target
		dx := target.X - enemy.X
		dy := target.Y - enemy.Y
		distance := math.Sqrt(dx*dx + dy*dy)

		// Get weapon range
		weaponRange := 30.0 // Default range
		if enemy.Weapon != nil {
			weaponRange = float64(enemy.Weapon.Range * 25) // Scale range like client
		}

		if distance > weaponRange {
			// Move toward target
			s.moveEnemyTowardTarget(enemy, target, distance, dx, dy)
		} else {
			// In range - attack if enough time has passed
			s.attemptEnemyAttack(enemy, target)
		}
	}
}

// addRangeThreat adds threat for players within range
func (s *GameServer) addRangeThreat(enemy *types.Enemy) {
	aggroRange := 100.0 // Aggro range in pixels
	rangeThreat := 0.1   // Small threat per tick for being in range
	
	for _, player := range s.players {
		dx := player.X - enemy.X
		dy := player.Y - enemy.Y
		distance := math.Sqrt(dx*dx + dy*dy)
		
		if distance <= aggroRange {
			// Add small threat for being in range
			enemy.ThreatList[player.ID] += rangeThreat
			
			// Update target if this creates a new highest threat
			s.updateEnemyTarget(enemy)
		}
	}
}

// findNearbyTarget looks for players within aggro range
func (s *GameServer) findNearbyTarget(enemy *types.Enemy) {
	// First check if we have anyone on threat list from range threat
	s.updateEnemyTarget(enemy)
}

// moveEnemyTowardTarget moves enemy toward its target
func (s *GameServer) moveEnemyTowardTarget(enemy *types.Enemy, target *types.Player, distance, dx, dy float64) {
	moveSpeed := 2.0 // Enemy move speed
	
	if distance > 0 {
		// Calculate new position
		newX := enemy.X + (dx/distance)*moveSpeed
		newY := enemy.Y + (dy/distance)*moveSpeed
		
		// Use sliding collision detection
		validX, validY := game.CheckWallCollisionWithSliding(enemy.X, enemy.Y, newX, newY, s.room.Walls)
		
		// Only update if position actually changed
		if validX != enemy.X || validY != enemy.Y {
			enemy.X = validX
			enemy.Y = validY
			
			// Broadcast enemy position update
			s.broadcast <- types.Message{
				Type: types.MsgEnemyUpdate,
				Data: s.marshal(enemy),
			}
		}
	}
}

// attemptEnemyAttack handles enemy attacks on players
func (s *GameServer) attemptEnemyAttack(enemy *types.Enemy, target *types.Player) {
	weaponDelay := 2 * time.Second // Default attack speed
	if enemy.Weapon != nil {
		weaponDelay = enemy.Weapon.Delay
	}
	
	if time.Since(enemy.LastAttack) > weaponDelay {
		// Calculate damage
		damage := 2 // Default damage
		if enemy.Weapon != nil {
			damage = enemy.Weapon.Damage
		}
		
		// Apply damage to player
		target.Health -= damage
		enemy.LastAttack = time.Now()
		
		// Generate rage for taking damage (warriors only)
		if target.Class == "warrior" {
			rageGain := 3 // Base rage gained per hit taken
			if target.Mana < target.MaxMana {
				target.Mana = min(target.MaxMana, target.Mana + rageGain)
			}
		}
		
		log.Printf("Enemy %s attacked player %s for %d damage (HP: %d/%d)",
			enemy.Name, target.Name, damage, target.Health, target.MaxHealth)
		
		// Broadcast player health and rage update
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerUpdate,
			PlayerID: target.ID,
			Data:     s.marshal(target),
		}
		
		// If player dies, clear enemy target and threat
		if target.Health <= 0 {
			delete(enemy.ThreatList, target.ID)
			enemy.TargetID = ""
			s.updateEnemyTarget(enemy) // Try to find new target
			log.Printf("Player %s has been defeated by %s", target.Name, enemy.Name)
		}
	}
}

