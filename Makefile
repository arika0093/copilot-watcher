.PHONY: build fmt lint vet test clean install check

BINARY := copilot-watcher
GO_FILES := $(shell find . -name '*.go' -not -path './vendor/*')

build:
	go build -o $(BINARY) .

fmt:
	gofmt -w $(GO_FILES)

lint: fmt
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -f $(BINARY)

install: build
	mv $(BINARY) $(GOPATH)/bin/$(BINARY)

# Run all checks (CI mode)
check: vet test
	golangci-lint run ./...
