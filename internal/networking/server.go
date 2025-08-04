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

type GameServer struct {
	players   map[string]*types.Player
	enemies   map[string]*types.Enemy
	room      types.Room
	mutex     sync.RWMutex
	upgrader  websocket.Upgrader
	broadcast chan types.Message
}

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

	go server.handleBroadcast()
	go server.handleEnemyAI()
	go server.handleRageDecay()

	return server
}

func (s *GameServer) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}

	playerID := uuid.New().String()
	weaponID := uuid.New().String()
	player := &types.Player{
		ID:        playerID,
		Name:      "Player " + playerID[:8],
		X:         400,
		Y:         300,
		Class:     "warrior",
		Health:    100,
		MaxHealth: 100,
		Mana:      0,
		MaxMana:   100,
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

	s.mutex.RLock()
	for existingPlayerID, existingPlayer := range s.players {
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

	s.broadcast <- types.Message{
		Type:     types.MsgPlayerJoin,
		PlayerID: playerID,
		Data:     s.marshal(player),
	}

	go s.handlePlayerConnection(player)
}

func (s *GameServer) handlePlayerConnection(player *types.Player) {
	defer func() {
		s.mutex.Lock()
		delete(s.players, player.ID)
		s.mutex.Unlock()

		player.Conn.Close()

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

		s.handleMessage(player, msg)
	}
}

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

		s.mutex.Lock()
		validX, validY := game.CheckWallCollisionWithSliding(player.X, player.Y, moveData.X, moveData.Y, s.room.Walls)
		player.X = validX
		player.Y = validY
		s.mutex.Unlock()

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

func (s *GameServer) handleBroadcast() {
	for msg := range s.broadcast {
		s.mutex.RLock()
		for _, player := range s.players {
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

func (s *GameServer) GetConnectedPlayers() int {
	s.mutex.RLock()
	defer s.mutex.RUnlock()
	return len(s.players)
}

func (s *GameServer) spawnInitialEnemies() {
	for i := 0; i < 3; i++ {
		enemyID := uuid.New().String()
		enemy := &types.Enemy{
			ID:         enemyID,
			Name:       "Enemy " + enemyID[:8],
			X:          200 + float64(i*300),
			Y:          200 + float64(i*150),
			EnemyType:  "basic",
			Health:     50,
			MaxHealth:  50,
			TargetID:   "",
			ThreatList: make(map[string]float64),
			Weapon: &types.Weapon{
				ID:         uuid.New().String(),
				Name:       "Claws",
				Damage:     10,
				Range:      1,
				WeaponType: "melee",
				Delay:      2 * time.Second,
			},
		}

		s.enemies[enemyID] = enemy
		log.Printf("Spawned enemy %s at (%.0f, %.0f)", enemy.Name, enemy.X, enemy.Y)

		s.broadcast <- types.Message{
			Type: types.MsgEnemySpawn,
			Data: s.marshal(enemy),
		}
	}
}

func (s *GameServer) handleRageDecay() {
	ticker := time.NewTicker(2 * time.Second) // Check every 2 seconds
	defer ticker.Stop()

	for range ticker.C {
		s.mutex.Lock()
		
		for _, player := range s.players {
			if player.Class == "warrior" && player.Mana > 0 {
				// Simple decay: lose 2 rage every tick (every 2 seconds)
				rageDecay := 2
				
				oldRage := player.Mana
				player.Mana -= rageDecay
				
				if player.Mana < 0 {
					player.Mana = 0
				}
				
				// Only broadcast if rage actually changed
				if player.Mana != oldRage {
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

func (s *GameServer) marshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

func (s *GameServer) handleCriticalStrike(attacker *types.Player, targetEnemyID string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	if attacker.Class != "warrior" {
		log.Printf("Player %s (%s) attempted critical strike but is not a warrior", attacker.Name, attacker.Class)
		return
	}

	rageCost := 30 // Critical strike costs 30 rage
	if attacker.Mana < rageCost {
		log.Printf("Player %s attempted critical strike but lacks rage (%d/%d)", attacker.Name, attacker.Mana, rageCost)
		return
	}

	enemy, exists := s.enemies[targetEnemyID]
	if !exists {
		log.Printf("Critical Strike: Enemy %s not found", targetEnemyID)
		return
	}

	attacker.Mana -= rageCost

	// Calculate critical damage (2x normal damage + bonus)
	baseDamage := 1
	if attacker.Weapon != nil {
		baseDamage = attacker.Weapon.Damage
	}
	critDamage := (baseDamage * 2) + 3 // 2x damage + 3 bonus

	enemy.Health -= critDamage
	log.Printf("Player %s used Critical Strike on %s for %d damage (HP: %d/%d)",
		attacker.Name, enemy.Name, critDamage, enemy.Health, enemy.MaxHealth)

	enemy.ThreatList[attacker.ID] += float64(critDamage)
	
	s.updateEnemyTarget(enemy)

	s.broadcast <- types.Message{
		Type:     types.MsgPlayerUpdate,
		PlayerID: attacker.ID,
		Data:     s.marshal(attacker),
	}

	if enemy.Health <= 0 {
			delete(s.enemies, targetEnemyID)
		log.Printf("Enemy %s has been defeated by %s's critical strike!", enemy.Name, attacker.Name)

		s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: s.marshal(map[string]interface{}{
				"id":   targetEnemyID,
				"dead": true,
			}),
		}
	} else {
			s.broadcast <- types.Message{
			Type: types.MsgEnemyUpdate,
			Data: s.marshal(enemy),
		}
	}
}

func (s *GameServer) handleCombat(attacker *types.Player, targetEnemyID string) {
	s.mutex.Lock()
	defer s.mutex.Unlock()

	enemy, exists := s.enemies[targetEnemyID]
	if !exists {
		log.Printf("Combat: Enemy %s not found", targetEnemyID)
		return
	}

	damage := 1 // Default damage
	if attacker.Weapon != nil {
		damage = attacker.Weapon.Damage
	}

	enemy.Health -= damage
	log.Printf("Player %s attacked %s for %d damage (HP: %d/%d)",
		attacker.Name, enemy.Name, damage, enemy.Health, enemy.MaxHealth)

	if attacker.Class == "warrior" {
		rageGain := 5 // Base rage gained per attack
		if attacker.Mana < attacker.MaxMana {
			attacker.Mana = min(attacker.MaxMana, attacker.Mana + rageGain)
			
			s.broadcast <- types.Message{
				Type:     types.MsgPlayerUpdate,
				PlayerID: attacker.ID,
				Data:     s.marshal(attacker),
			}
		}
	}

	enemy.ThreatList[attacker.ID] += float64(damage)
	
	s.updateEnemyTarget(enemy)

	if enemy.Health <= 0 {
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
	
	// Find player with highest threat that still exists and is alive
	for playerID, threat := range enemy.ThreatList {
		if player, exists := s.players[playerID]; exists && !player.Dead && threat > highestThreat {
			highestThreat = threat
			newTargetID = playerID
		}
	}
	
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

func (s *GameServer) handleEnemyAI() {
	ticker := time.NewTicker(100 * time.Millisecond) // Update AI 10 times per second
	defer ticker.Stop()

	for range ticker.C {
		s.mutex.Lock()
		
		for _, enemy := range s.enemies {
			s.processEnemyAI(enemy)
		}
		
		s.mutex.Unlock()
	}
}

func (s *GameServer) processEnemyAI(enemy *types.Enemy) {
	// Clean up threat list - remove disconnected or dead players
	for playerID := range enemy.ThreatList {
		if player, exists := s.players[playerID]; !exists || player.Dead {
			delete(enemy.ThreatList, playerID)
		}
	}
	
	s.addRangeThreat(enemy)
	
	if enemy.TargetID == "" {
		s.findNearbyTarget(enemy)
	} else {
		target, exists := s.players[enemy.TargetID]
		if !exists {
			enemy.TargetID = ""
			s.updateEnemyTarget(enemy) // Try to find new target from threat list
			return
		}

		dx := target.X - enemy.X
		dy := target.Y - enemy.Y
		distance := math.Sqrt(dx*dx + dy*dy)

		weaponRange := 30.0 // Default range
		if enemy.Weapon != nil {
			weaponRange = float64(enemy.Weapon.Range * 25) // Scale range like client
		}

		if distance > weaponRange {
			s.moveEnemyTowardTarget(enemy, target, distance, dx, dy)
		} else {
			s.attemptEnemyAttack(enemy, target)
		}
	}
}

func (s *GameServer) addRangeThreat(enemy *types.Enemy) {
	aggroRange := 100.0 // Aggro range in pixels
	rangeThreat := 0.1   // Small threat per tick for being in range
	
	for _, player := range s.players {
		// Skip dead players
		if player.Dead {
			continue
		}
		
		dx := player.X - enemy.X
		dy := player.Y - enemy.Y
		distance := math.Sqrt(dx*dx + dy*dy)
		
		if distance <= aggroRange {
			enemy.ThreatList[player.ID] += rangeThreat
			
			s.updateEnemyTarget(enemy)
		}
	}
}

func (s *GameServer) findNearbyTarget(enemy *types.Enemy) {
	s.updateEnemyTarget(enemy)
}

func (s *GameServer) moveEnemyTowardTarget(enemy *types.Enemy, target *types.Player, distance, dx, dy float64) {
	moveSpeed := 2.0 // Enemy move speed
	
	if distance > 0 {
		newX := enemy.X + (dx/distance)*moveSpeed
		newY := enemy.Y + (dy/distance)*moveSpeed
		
		validX, validY := game.CheckWallCollisionWithSliding(enemy.X, enemy.Y, newX, newY, s.room.Walls)
		
		if validX != enemy.X || validY != enemy.Y {
			enemy.X = validX
			enemy.Y = validY
			
			s.broadcast <- types.Message{
				Type: types.MsgEnemyUpdate,
				Data: s.marshal(enemy),
			}
		}
	}
}

func (s *GameServer) attemptEnemyAttack(enemy *types.Enemy, target *types.Player) {
	// Don't attack dead players
	if target.Dead {
		return
	}
	
	weaponDelay := 2 * time.Second // Default attack speed
	if enemy.Weapon != nil {
		weaponDelay = enemy.Weapon.Delay
	}
	
	if time.Since(enemy.LastAttack) > weaponDelay {
		damage := 2 // Default damage
		if enemy.Weapon != nil {
			damage = enemy.Weapon.Damage
		}
		
		target.Health -= damage
		enemy.LastAttack = time.Now()
		
		if target.Class == "warrior" {
			rageGain := 3 // Base rage gained per hit taken
			if target.Mana < target.MaxMana {
				target.Mana = min(target.MaxMana, target.Mana + rageGain)
			}
		}
		
		log.Printf("Enemy %s attacked player %s for %d damage (HP: %d/%d)",
			enemy.Name, target.Name, damage, target.Health, target.MaxHealth)
		
		s.broadcast <- types.Message{
			Type:     types.MsgPlayerUpdate,
			PlayerID: target.ID,
			Data:     s.marshal(target),
		}
		
		if target.Health <= 0 {
			target.Health = 0
			target.Dead = true
			delete(enemy.ThreatList, target.ID)
			enemy.TargetID = ""
			s.updateEnemyTarget(enemy) // Try to find new target
			log.Printf("Player %s has been defeated by %s", target.Name, enemy.Name)
			
			// Send death message to all players
			s.broadcast <- types.Message{
				Type:     types.MsgPlayerUpdate,
				PlayerID: target.ID,
				Data:     s.marshal(target),
			}
		}
	}
}

