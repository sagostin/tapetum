VERSION ?= dev

.PHONY: build run web web-dev vet test clean

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o tapetumd ./cmd/tapetumd

run: ## run locally (requires Postgres, see docker compose)
	TAPETUM_DEV=true go run -ldflags "-X main.version=$(VERSION)" ./cmd/tapetumd

web: ## build the frontend into web/dist
	cd web && npm run build

web-dev: ## vite dev server with proxy to :8080
	cd web && npm run dev

vet:
	go vet ./...

test:
	go test ./...

clean:
	rm -f tapetumd
	rm -rf web/dist/assets web/dist/*.js web/dist/*.css
