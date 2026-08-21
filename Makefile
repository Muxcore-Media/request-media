.PHONY: build test lint clean proto tidy

GO ?= go
BINARY ?= request-media

build:
	$(GO) build -o $(BINARY) ./cmd/module

test:
	$(GO) test -race -count=1 -timeout 60s ./...

lint:
	golangci-lint run --timeout 120s ./...

clean:
	rm -f $(BINARY)

proto:
	protoc --go_out=proto --go_opt=paths=source_relative --go-grpc_out=proto --go-grpc_opt=paths=source_relative -I proto proto/requestmedia/requestmedia.proto

tidy:
	$(GO) mod tidy

fmt:
	$(GO) fmt ./...
