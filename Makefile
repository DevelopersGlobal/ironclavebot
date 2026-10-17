BINARY_NAME=ironclavebot
BUILD_DIR=bin

.PHONY: all build run test clean docker-build

all: build

build:
	@mkdir -p $(BUILD_DIR)
	@echo "Building $(BINARY_NAME) (static, CGO_ENABLED=0)..."
	CGO_ENABLED=0 go build -ldflags="-w -s" -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/ironclavebot
	@echo "Build complete: $(BUILD_DIR)/$(BINARY_NAME)"

run: build
	./$(BUILD_DIR)/$(BINARY_NAME) serve -port 8080

test:
	go test -race -v ./...

clean:
	rm -rf $(BUILD_DIR) data/ workspace/

docker-build:
	docker build -t developersglobal/ironclavebot:latest .
