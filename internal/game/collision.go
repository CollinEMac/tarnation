package game

import "github.com/CollinEMac/tarnation/internal/types"

// CheckWallCollision checks if a position would collide with any walls
func CheckWallCollision(x, y float64, walls []types.Wall) bool {
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

// CheckWallCollisionWithSliding checks collision and returns valid position allowing sliding
func CheckWallCollisionWithSliding(oldX, oldY, newX, newY float64, walls []types.Wall) (float64, float64) {
	// If new position doesn't collide, allow the move
	if !CheckWallCollision(newX, newY, walls) {
		return newX, newY
	}
	
	// Try horizontal movement only (keep old Y)
	if !CheckWallCollision(newX, oldY, walls) {
		return newX, oldY
	}
	
	// Try vertical movement only (keep old X)
	if !CheckWallCollision(oldX, newY, walls) {
		return oldX, newY
	}
	
	// Can't move in any direction, stay at old position
	return oldX, oldY
}

// CreateDungeonRoom creates a larger rectangular room with walls for camera testing
func CreateDungeonRoom() types.Room {
	walls := []types.Wall{
		// Top wall
		{X: 0, Y: 0, Width: 1200, Height: 20},
		// Bottom wall
		{X: 0, Y: 880, Width: 1200, Height: 20},
		// Left wall
		{X: 0, Y: 0, Width: 20, Height: 900 },
		// Right wall
		{X: 1180, Y: 0, Width: 20, Height: 900 },
	}
	
	return types.Room{Walls: walls}
}
