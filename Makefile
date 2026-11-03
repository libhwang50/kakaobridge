# -tags goolm selects mautrix's pure-Go olm; without it the build needs C libolm headers
# (mautrix's crypto/registerlibolm.go pulls crypto/libolm). GOTMPDIR is set because the default
# scratch dir on some hosts is too small for the cgo link step.
GO ?= go
TAGS ?= goolm
export GOTMPDIR ?= /dev/shm

.PHONY: build test vet
build:
	$(GO) build -tags $(TAGS) ./...

test:
	$(GO) test -tags $(TAGS) ./...

vet:
	$(GO) vet -tags $(TAGS) ./...
