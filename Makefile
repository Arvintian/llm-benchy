BINARY  := llm-benchy
VERSION ?= 0.1.0
LDFLAGS := -s -w -X main.Version=$(VERSION)
GOFLAGS ?=

GOOS ?= linux
GOARCH ?= amd64

.PHONY: all build run check test vet fmt tidy install cross build-all release clean

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

# build-all: build binaries for all release platforms
build-all: clean
	@mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe .
	@echo "Build complete. Binaries are in dist/"

# release: create archives from build-all output
release: build-all
	@echo "Creating release archives..."
	cd dist && tar -czf $(BINARY)-linux-amd64.tar.gz $(BINARY)-linux-amd64
	cd dist && tar -czf $(BINARY)-linux-arm64.tar.gz $(BINARY)-linux-arm64
	cd dist && tar -czf $(BINARY)-darwin-amd64.tar.gz $(BINARY)-darwin-amd64
	cd dist && tar -czf $(BINARY)-darwin-arm64.tar.gz $(BINARY)-darwin-arm64
	cd dist && (command -v zip >/dev/null && zip -q $(BINARY)-windows-amd64.zip $(BINARY)-windows-amd64.exe \
		|| python3 -c "import zipfile,sys; z=zipfile.ZipFile(sys.argv[1],'w',zipfile.ZIP_DEFLATED); z.write(sys.argv[2]); z.close()" $(BINARY)-windows-amd64.zip $(BINARY)-windows-amd64.exe)
	@echo "Release archives created in dist/"

clean:
	rm -f $(BINARY)
	rm -rf dist
