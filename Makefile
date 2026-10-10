.PHONY: test build linux clean
test:
	go test -race ./...
	go vet ./...
build:
	mkdir -p bin
	go build -trimpath -o bin/panel ./cmd/panel
	go build -trimpath -o bin/agent ./cmd/agent
linux:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/panel-linux-amd64 ./cmd/panel
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o bin/agent-linux-amd64 ./cmd/agent
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/panel-linux-arm64 ./cmd/panel
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o bin/agent-linux-arm64 ./cmd/agent
clean:
	rm -rf bin
