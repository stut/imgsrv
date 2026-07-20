BINARY  := imgsrv
IMAGE   := ghcr.io/stut/imgsrv
VERSION := $(shell cat VERSION)

.PHONY: all test build docker vet fmt lint clean

all: test build

test:
	go test ./...

build:
	go build -ldflags "-X main.version=$(VERSION)" -o $(BINARY) ./cmd/imgsrv

# Local/manual image build. Releases are built and pushed by CI (see
# .github/workflows/release.yml). Built for linux/amd64 only; from an arm64
# Mac this cross-builds (one-time setup:
# docker run --privileged --rm tonistiigi/binfmt --install amd64).
docker:
	docker build --platform linux/amd64 --provenance=false \
		--build-arg VERSION=$(VERSION) \
		-t $(IMAGE):$(VERSION) . && docker push $(IMAGE):$(VERSION)

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	gofmt -w .

clean:
	rm -f $(BINARY)
