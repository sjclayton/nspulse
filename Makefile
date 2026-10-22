# Variables
BINARY_NAME=nspulse
OUTPUT_DIR=out
CMD_DIR=./cmd/nspulse
GO=go
GOFLAGS=-trimpath -ldflags="-s -w"
# UPX packing is on by default for release builds.
# Run `make build UPX=0` to skip it (e.g. upx not installed).
UPX=1

# Default target
all: build

# Build the binary (release: static, stripped, UPX-packed by default).
# Always rebuilds from scratch so re-running never tries to
# re-pack an already-packed binary (UPX refuses with AlreadyPackedException).
build:
	@echo "Building $(BINARY_NAME) (release)..."
	@mkdir -p $(OUTPUT_DIR)
	@rm -f $(OUTPUT_DIR)/$(BINARY_NAME)
	@CGO_ENABLED=0 $(GO) build $(GOFLAGS) -o $(OUTPUT_DIR)/$(BINARY_NAME) $(CMD_DIR)
ifeq ($(UPX),1)
	@echo "Packing with UPX..."
	@command -v upx >/dev/null 2>&1 || { echo "error: upx not found (install upx, or run 'make build UPX=0')"; exit 1; }
	@upx --best --lzma $(OUTPUT_DIR)/$(BINARY_NAME)
endif
	@echo "Built successfully in $(OUTPUT_DIR)/"

# Build the binary (release flags, no UPX packing)
build-bare:
	@$(MAKE) build UPX=0

# Build the binary (debug symbols)
debug:
	@echo "Building $(BINARY_NAME) (debug symbols)..."
	@mkdir -p $(OUTPUT_DIR)
	@$(GO) build -o $(OUTPUT_DIR)/$(BINARY_NAME) $(CMD_DIR)
	@echo "Built successfully in $(OUTPUT_DIR)/"

# Clean the output directory
clean:
	@echo "Cleaning up..."
	@rm -rf $(OUTPUT_DIR)
	@echo "Done cleaning."

# Run the application (with optional args)
run: build-bare
	@echo "Running $(BINARY_NAME) with args: $(filter-out run,$(MAKECMDGOALS))\n"
	@./$(OUTPUT_DIR)/$(BINARY_NAME) $(filter-out run,$(MAKECMDGOALS))

# Run all tests
test:
	@echo "Running tests..."
	@$(GO) test ./...

# Formatting, vet, and tests
check:
	@echo "Checking formatting..."
	@test -z "$$($(GO)fmt -l .)" || { $(GO)fmt -l .; exit 1; }
	@echo "Vetting..."
	@$(GO) vet ./...
	@$(GO) test ./...

# Install binary + systemd unit (needs root)
install: build
	@echo "Installing $(BINARY_NAME)..."
	@install -m 0755 $(OUTPUT_DIR)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)
	@install -m 0644 nspulse.service /etc/systemd/system/nspulse.service
	@systemctl daemon-reload
	@-systemctl try-restart nspulse
	@echo "Installed. Service restarted if it was running; enable with: systemctl enable --now nspulse"

.PHONY: all build build-bare debug clean run test check install
