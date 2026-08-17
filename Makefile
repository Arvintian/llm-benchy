BINARY  := llm-benchy
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.Version=$(VERSION)
GOFLAGS ?=

GOOS ?= linux
GOARCH ?= amd64

.PHONY: all build run check test vet fmt tidy install cross clean

all: build

build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build
	./$(BINARY) $(ARGS)

# check: verify the code - vet, build and unit tests
check: vet build test

test:
	go test $(GOFLAGS) ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

tidy:
	go mod tidy

install:
	go install $(GOFLAGS) -ldflags "$(LDFLAGS)" .

cross:
	GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 .
	GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 .

clean:
	rm -f $(BINARY)
	rm -rf dist
