# Сборка Soundpad.
#
#   make build   — собрать бинарник в bin/soundpad
#   make run     — запустить из исходников
#   make test    — прогнать тесты
#   make dist    — собрать для Linux/macOS/Windows

APP := soundpad
BIN := bin/$(APP)
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: build run test vet fmt dist clean

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BIN) .

run:
	go run .

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

dist:
	mkdir -p bin
	GOOS=linux   GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(APP)-linux-amd64 .
	GOOS=darwin  GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(APP)-darwin-arm64 .
	GOOS=darwin  GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(APP)-darwin-amd64 .
	GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o bin/$(APP)-windows-amd64.exe .

clean:
	rm -rf bin
