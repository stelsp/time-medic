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
	shellcheck bin/timewatch

install: build
	install -d $(HOME)/.local/bin
	install -m 0755 bin/timetop $(HOME)/.local/bin/timetop
	install -m 0755 bin/timewatch $(HOME)/.local/bin/timewatch
	@echo "installed timetop and timewatch to $(HOME)/.local/bin"
