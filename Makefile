PLUGIN_ID=com.mattermost.songlink
PLUGIN_VERSION=0.1.0
BUNDLE_NAME=$(PLUGIN_ID)-$(PLUGIN_VERSION).tar.gz

all: dist

server/dist/plugin-linux-amd64:
	cd server && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/plugin-linux-amd64 .

server/dist/plugin-linux-arm64:
	cd server && GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/plugin-linux-arm64 .

bundle: server/dist/plugin-linux-amd64 server/dist/plugin-linux-arm64
	mkdir -p dist
	tar -czf dist/$(BUNDLE_NAME) plugin.json server/dist/plugin-linux-amd64 server/dist/plugin-linux-arm64

# --- Docker-based build ---
DOCKER_IMAGE=golang:1.22-bookworm

docker-build:
	docker run --rm -v "$$PWD":/workspace -w /workspace $(DOCKER_IMAGE) bash -lc 'set -euo pipefail; cd server; mkdir -p dist; go mod download; GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o dist/plugin-linux-amd64 .; GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o dist/plugin-linux-arm64 .'

docker-bundle: docker-build
	docker run --rm -v "$$PWD":/workspace -w /workspace $(DOCKER_IMAGE) bash -lc 'set -euo pipefail; mkdir -p dist; tar -czf dist/$(BUNDLE_NAME) plugin.json server/dist/plugin-linux-amd64 server/dist/plugin-linux-arm64'

clean:
	rm -rf server/dist dist

.PHONY: all bundle clean docker-build docker-bundle

dist: bundle

docker: docker-bundle
