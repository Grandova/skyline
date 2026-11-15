BINARY      := skyline
LDFLAGS     := -s -w
BUILD_FLAGS := -trimpath -ldflags="$(LDFLAGS)"

.PHONY: all build build-all linux-amd64 linux-arm64 test clean install

all: linux-amd64

build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY) .

linux-amd64:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o $(BINARY) .

linux-arm64:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -o $(BINARY)-linux-arm64 .

build-all:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(BUILD_FLAGS) -o dist/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(BUILD_FLAGS) -o dist/$(BINARY)-linux-arm64 .

test:
	go test -v ./...

clean:
	rm -rf dist $(BINARY)-linux-amd64 $(BINARY)-linux-arm64

install: build
	install -d /etc/skyline
	install -m 0755 $(BINARY) /usr/local/bin/$(BINARY)
	@if [ ! -f /etc/skyline/config.yml ]; then \
		install -m 0600 config.example.yml /etc/skyline/config.yml; \
	fi
	install -m 0644 skyline.service /etc/systemd/system/skyline.service
	systemctl daemon-reload
