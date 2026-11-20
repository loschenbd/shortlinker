.PHONY: build test install swiftbar

build:
	go build -o bin/shortlinker .

test:
	go vet ./...
	go test -race ./...

install:
	./install.sh

swiftbar:
	./install.sh swiftbar
