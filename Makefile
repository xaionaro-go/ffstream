
all: ffstream-linux-amd64 ffstream-linux-arm64 ffstreamctl-linux-amd64 ffstreamctl-linux-arm64

build:
	mkdir -p build

ffstream-linux-amd64: build
	GOOS=linux GOARCH=amd64 go build -o build/ffstream-linux-amd64 ./cmd/ffstream

ffstream-linux-arm64: build
	GOOS=linux GOARCH=arm64 go build -o build/ffstream-linux-arm64 ./cmd/ffstream

ffstreamctl-linux-amd64: build
	GOOS=linux GOARCH=amd64 go build -o build/ffstreamctl-linux-amd64 ./cmd/ffstreamctl

ffstreamctl-linux-arm64: build
	GOOS=linux GOARCH=arm64 go build -o build/ffstreamctl-linux-arm64 ./cmd/ffstreamctl

