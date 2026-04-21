.PHONY: build run test clean

BINARY_NAME=gateway
BUILD_DIR=./bin
CMD_DIR=./cmd/gateway

build:
	mkdir -p $(BUILD_DIR)
	go build -o $(BUILD_DIR)/$(BINARY_NAME) $(CMD_DIR)

run: build
	$(BUILD_DIR)/$(BINARY_NAME)

test:
	go test ./...

clean:
	rm -rf $(BUILD_DIR)
	go clean
