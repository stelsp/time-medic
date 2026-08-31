.PHONY: build test clean lint install

build:
	go build -o bin/timetop ./cmd/timetop

test:
	go test ./...

clean:
	rm -f bin/timetop

lint:
	gofmt -l cmd/
	go vet ./...

install: build
	install -d $(HOME)/.local/bin
	install -m 0755 bin/timetop $(HOME)/.local/bin/timetop
	@echo "installed to $(HOME)/.local/bin/timetop"
