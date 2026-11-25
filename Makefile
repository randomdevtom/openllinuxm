VERSION ?= 0.2.0
LDFLAGS := -s -w -X main.version=$(VERSION)

openllinuxm: $(wildcard *.go) go.mod
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o $@ .

install: openllinuxm
	install -Dm755 openllinuxm $(HOME)/.local/bin/openllinuxm

dist:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/openllinuxm-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/openllinuxm-linux-arm64 .

.PHONY: install dist
