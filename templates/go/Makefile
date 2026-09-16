.PHONY: build clean stop start restart test

# srv/ is a package directory, so -o srv writes the binary to srv/srv.
build:
	go build -o srv ./cmd/srv

clean:
	rm -f srv/srv

test:
	go test ./...
