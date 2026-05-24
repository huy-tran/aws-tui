VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X main.commit=$(COMMIT) \
	-X main.date=$(DATE)

.PHONY: run build install smoke vet clean dist

run:
	go run ./cmd/aws-tui

build:
	go build -ldflags="$(LDFLAGS)" -o bin/aws-tui ./cmd/aws-tui

# Install into $GOBIN (or $GOPATH/bin) so `aws-tui` resolves on PATH.
# Re-run after tagging a new version to bake the new tag into the title.
install:
	go install -ldflags="$(LDFLAGS)" ./cmd/aws-tui

smoke:
	go build -o bin/smoke ./cmd/smoke
	./bin/smoke

vet:
	go vet ./...

clean:
	rm -rf bin/ dist/

dist:
	GOOS=windows GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/aws-tui-windows-amd64.exe ./cmd/aws-tui
	GOOS=darwin  GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/aws-tui-darwin-amd64       ./cmd/aws-tui
	GOOS=darwin  GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/aws-tui-darwin-arm64       ./cmd/aws-tui
	GOOS=linux   GOARCH=amd64 go build -ldflags="$(LDFLAGS)" -o dist/aws-tui-linux-amd64        ./cmd/aws-tui
	GOOS=linux   GOARCH=arm64 go build -ldflags="$(LDFLAGS)" -o dist/aws-tui-linux-arm64        ./cmd/aws-tui
