GO_IMAGE ?= golang:1.26-bookworm
DOCKER_PLATFORM ?= linux/amd64
VERSION ?= 0.2.0
PLUGIN_DIR := build/plugins/linux/amd64
PLUGIN_SO := $(PLUGIN_DIR)/cliproxyapi-cursor.so
CACHE_DIR := .cache
VERSION_LDFLAG := -X main.pluginVersion=$(VERSION)

.PHONY: test build build-local package clean

test:
	go test ./...

build:
	mkdir -p $(PLUGIN_DIR) $(CACHE_DIR)/go-build $(CACHE_DIR)/go-mod $(CACHE_DIR)/home
	docker run --rm --platform $(DOCKER_PLATFORM) \
		--user "$$(id -u):$$(id -g)" \
		-e HOME=/src/$(CACHE_DIR)/home \
		-e GOCACHE=/src/$(CACHE_DIR)/go-build \
		-e GOMODCACHE=/src/$(CACHE_DIR)/go-mod \
		-v "$(CURDIR):/src" \
		-w /src \
		$(GO_IMAGE) \
		sh -ec 'CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/cliproxyapi-cursor'

build-local:
	mkdir -p $(PLUGIN_DIR)
	CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -buildvcs=false -trimpath -ldflags "$(VERSION_LDFLAG)" -buildmode=c-shared -o $(PLUGIN_SO) ./cmd/cliproxyapi-cursor

package: build
	scripts/package-release.sh "$(VERSION)"

clean:
	rm -rf build dist $(CACHE_DIR)
