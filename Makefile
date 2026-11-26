VERSION ?= 0.3.1
LDFLAGS := -s -w -X main.version=$(VERSION)

openllinuxm: $(shell find cmd internal -name "*.go") go.mod
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ ./cmd/openllinuxm

install: openllinuxm
	install -Dm755 openllinuxm $(HOME)/.local/bin/openllinuxm

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/openllinuxm-linux-amd64 ./cmd/openllinuxm
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/openllinuxm-linux-arm64 ./cmd/openllinuxm

.PHONY: install dist
