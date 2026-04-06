.PHONY: build test lint clean

BIN_DIR := bin

build:
	go build -o $(BIN_DIR)/api ./cmd/api
	go build -o $(BIN_DIR)/controller ./cmd/controller
	go build -o $(BIN_DIR)/bgp ./cmd/bgp
	go build -o $(BIN_DIR)/agent ./cmd/agent

test:
	go test -v -race -count=1 ./...

test-coverage:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html
	@echo "Coverage report: coverage.html"

lint:
	go vet ./...

clean:
	rm -rf $(BIN_DIR) coverage.out coverage.html
