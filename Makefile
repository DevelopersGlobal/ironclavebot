BINARY_NAME=ironclavebot
BUILD_DIR=bin

.PHONY: all build run test clean reset-data docker-build

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
	rm -rf $(BUILD_DIR)

docker-build:
	docker build -t developersglobal/ironclavebot:latest .

# Destructive: deletes the conversation database and the sandbox workspace.
# Never run as part of a normal clean.
reset-data:
	@echo "This deletes ./data and ./workspace permanently."
	@printf "Type 'yes' to continue: " && read ans && [ "$$ans" = "yes" ]
	rm -rf data/ workspace/
