BINARY_SERVER := interactd
BINARY_CLIENT := interact
MODULE        := github.com/newuser-admin/claude/interact

LDFLAGS := -s -w
GOFLAGS := -trimpath

.PHONY: all build server client tidy clean install

all: tidy build

build: server client

server:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY_SERVER) ./cmd/interactd

client:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(BINARY_CLIENT) ./cmd/interact

# Cross-compile for all major targets
release:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_SERVER)-linux-amd64   ./cmd/interactd
	GOOS=linux   GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_CLIENT)-linux-amd64   ./cmd/interact
	GOOS=darwin  GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_SERVER)-darwin-amd64  ./cmd/interactd
	GOOS=darwin  GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_CLIENT)-darwin-amd64  ./cmd/interact
	GOOS=darwin  GOARCH=arm64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_SERVER)-darwin-arm64  ./cmd/interactd
	GOOS=darwin  GOARCH=arm64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_CLIENT)-darwin-arm64  ./cmd/interact
	GOOS=windows GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_SERVER)-windows-amd64.exe ./cmd/interactd
	GOOS=windows GOARCH=amd64  go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY_CLIENT)-windows-amd64.exe ./cmd/interact

tidy:
	go mod tidy

test:
	go test ./...

clean:
	rm -rf bin/ dist/

install: build
	cp bin/$(BINARY_SERVER) /usr/local/bin/
	cp bin/$(BINARY_CLIENT)  /usr/local/bin/
