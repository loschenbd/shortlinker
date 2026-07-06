.PHONY: build test install

build:
	go build -o bin/shortlinker .

test:
	go vet ./...
	go test ./...

install:
	./install.sh
