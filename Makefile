# Tarnation Game Development Makefile

# Variables
BINARY_DIR = bin
SERVER_BINARY = $(BINARY_DIR)/tarnation-server
CLIENT_BINARY = $(BINARY_DIR)/tarnation-client

# Default target
.PHONY: all
all: build

# Create binary directory
$(BINARY_DIR):
	mkdir -p $(BINARY_DIR)

# Build server
.PHONY: build-server
build-server: $(BINARY_DIR)
	go build -o $(SERVER_BINARY) ./cmd/server

# Build client
.PHONY: build-client
build-client: $(BINARY_DIR)
	go build -o $(CLIENT_BINARY) ./cmd/client

# Build both
.PHONY: build
build: build-server build-client

# Run server
.PHONY: run-server
run-server: build-server
	./$(SERVER_BINARY)

# Run client
.PHONY: run-client
run-client: build-client
	./$(CLIENT_BINARY)

# Clean built binaries
.PHONY: clean
clean:
	rm -rf $(BINARY_DIR)

# Install dependencies
.PHONY: deps
deps:
	go mod tidy
	go mod download

# Format code
.PHONY: fmt
fmt:
	go fmt ./...

# Test
.PHONY: test
test:
	go test ./...

# Cross-compile for different platforms
.PHONY: build-all
build-all: $(BINARY_DIR)
	# Linux
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-server-linux ./cmd/server
	GOOS=linux GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-client-linux ./cmd/client
	
	# Windows
	GOOS=windows GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-server.exe ./cmd/server
	GOOS=windows GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-client.exe ./cmd/client
	
	# macOS
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-server-mac ./cmd/server
	GOOS=darwin GOARCH=amd64 go build -o $(BINARY_DIR)/tarnation-client-mac ./cmd/client

# Run both server and client together (server in background)
.PHONY: run
run: build
	@echo "Starting server in background..."
	@./$(SERVER_BINARY) & 
	@SERVER_PID=$$!; \
	echo "Server started with PID $$SERVER_PID"; \
	echo "Waiting 2 seconds for server to initialize..."; \
	sleep 2; \
	echo "Starting client..."; \
	./$(CLIENT_BINARY); \
	echo "Client stopped, killing server..."; \
	kill $$SERVER_PID 2>/dev/null || true

# Development: run server and client in separate terminals
.PHONY: dev
dev:
	@echo "Starting development environment..."
	@echo "Run 'make run-server' in one terminal"
	@echo "Run 'make run-client' in another terminal"

# Help
.PHONY: help
help:
	@echo "Tarnation Development Commands:"
	@echo "  build         - Build both server and client"
	@echo "  build-server  - Build server only"
	@echo "  build-client  - Build client only"
	@echo "  run-server    - Build and run server"
	@echo "  run-client    - Build and run client"
	@echo "  run     			 - Build and run server + client together"
	@echo "  build-all     - Cross-compile for Linux, Windows, and macOS"
	@echo "  clean         - Remove built binaries"
	@echo "  deps          - Install/update dependencies"
	@echo "  fmt           - Format Go code"
	@echo "  test          - Run tests"
	@echo "  dev           - Show development setup instructions"
	@echo "  help          - Show this help message"
