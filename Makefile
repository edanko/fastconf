.PHONY: all build test race integration vet fmt lint bench cover tidy

all: fmt vet test build

build:
	go build -o bin/fastconf-server ./cmd/fastconf-server

test:
	go test ./...

race:
	go test -race -count=1 -shuffle=on ./...

integration:
	go test -race -tags=integration -count=1 -v ./test/integration

vet:
	go vet ./...

fmt:
	gofmt -w .

lint:
	golangci-lint run --timeout=5m

bench:
	go test -run '^$$' -bench . -benchmem -count=10 ./... | tee bench.txt
	benchstat bench.txt

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out

tidy:
	go mod tidy
