# Go parameters
GOCMD=go
GOBUILD=$(GOCMD) build
GOCLEAN=$(GOCMD) clean
GOTEST=$(GOCMD) test
GOGET=$(GOCMD) get

# Go files
SERVER_GO_FILES := $(shell find cmd/server server common -name '*.go')
CLIENT_GO_FILES := $(shell find cmd/client client common -name '*.go')

# Binaries
SERVER_BINARY=frps
CLIENT_BINARY=frpc

# Directories
BIN_DIR=bin

all: build

# Hardened production build (stripped, trimmed) - set via: make build-prod
build-prod: $(SERVER_GO_FILES) $(CLIENT_GO_FILES)
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -trimpath -ldflags "-s -w" -buildvcs=false -o $(BIN_DIR)/$(SERVER_BINARY) ./cmd/server
	$(GOBUILD) -trimpath -ldflags "-s -w" -buildvcs=false -o $(BIN_DIR)/$(CLIENT_BINARY) ./cmd/client

# Race build for testing (not for production)
build-race: $(SERVER_GO_FILES) $(CLIENT_GO_FILES)
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -race -o $(BIN_DIR)/$(SERVER_BINARY)-race ./cmd/server
	$(GOBUILD) -race -o $(BIN_DIR)/$(CLIENT_BINARY)-race ./cmd/client

build: $(BIN_DIR)/$(SERVER_BINARY) $(BIN_DIR)/$(CLIENT_BINARY)

$(BIN_DIR)/$(SERVER_BINARY): $(SERVER_GO_FILES)
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(BIN_DIR)/$(SERVER_BINARY) ./cmd/server

$(BIN_DIR)/$(CLIENT_BINARY): $(CLIENT_GO_FILES)
	@mkdir -p $(BIN_DIR)
	$(GOBUILD) -o $(BIN_DIR)/$(CLIENT_BINARY) ./cmd/client

clean:
	rm -rf $(BIN_DIR)

.PHONY: all build clean
