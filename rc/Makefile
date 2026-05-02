
build:
	GOOS=js GOARCH=wasm go build -o rc.wasm ./cmd/rc
.PHONY: build

dist:
	GOOS=js GOARCH=wasm go build -o ../dist/rc.wasm ./cmd/rc
.PHONY: dist

clean:
	rm -f rc.wasm
.PHONY: clean