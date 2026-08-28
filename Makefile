VERSION ?= dev

.PHONY: build run dev web web-dev vet test test-race clean

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o tapetumd ./cmd/tapetumd

run: ## run locally (requires Postgres, see docker compose)
	TAPETUM_DEV=true go run -ldflags "-X main.version=$(VERSION)" ./cmd/tapetumd

dev: ## dev: start backend + vite together, restart backend on Go changes
	@if [ ! -f config.yaml ]; then cp -n config.example.yaml config.yaml && echo "wrote config.yaml from example"; fi
	@trap 'kill 0' EXIT; \
	TAPETUM_DEV=true TAPETUM_ADDR=":8080" \
	  go run -ldflags "-X main.version=$(VERSION)" ./cmd/tapetumd & \
	cd web && npm run dev

web: ## build the frontend into web/dist
	cd web && npm run build

web-dev: ## vite dev server only (proxy to :8080)
	cd web && npm run dev

vet:
	go vet ./...

test:
	go test ./...

test-race: ## race detector (needs Postgres + ffmpeg on PATH)
	go test -race ./...

clean:
	rm -f tapetumd
	rm -rf web/dist/assets web/dist/*.js web/dist/*.css
