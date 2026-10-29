.PHONY: infra migrate build test web-test sim proto web

infra:
	docker compose up -d --wait redis postgres jaeger
	docker compose run --rm migrate

build:
	go build -o bin/ ./cmd/location ./cmd/matching ./cmd/gateway ./cmd/sim

test:
	go vet ./...
	go test -race -count=1 ./...

web-test:
	cd web && npm test && npm run build

proto:
	buf lint && buf generate

# Run each of these in its own terminal after `make infra build`.
# Set OTLP_ENDPOINT=localhost:4317 to send traces to Jaeger.
sim:
	./bin/sim --drivers 200 --riders 600 --rate 10
