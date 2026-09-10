.PHONY: build-daemon build-mac test test-go test-mac release
build-daemon:
	cd daemon && CGO_ENABLED=0 go build -trimpath -o ../build/deaddrop ./cmd/deaddrop
build-mac:
	xcodebuild -project mac/DeadDrop.xcodeproj -scheme DeadDrop -configuration Release -derivedDataPath build/DerivedData CODE_SIGNING_ALLOWED=NO build
test: test-go test-mac
test-go:
	cd daemon && go test -race ./... && go vet ./...
test-mac:
	cd mac/DeadDropKit && swift test
release:
	goreleaser release --clean
