.PHONY: help edge-install edge-dev edge-deploy edge-tail edge-setup healer-run healer-build healer-docker compose-up compose-down ci check-kv

help:
	@echo "AutoFix targets:"
	@echo "  make edge-install   - npm ci in edge-worker"
	@echo "  make edge-setup     - create KV + queues and patch wrangler.toml"
	@echo "  make check-kv       - validate KV ids in wrangler.toml"
	@echo "  make edge-dev       - wrangler dev"
	@echo "  make edge-deploy    - wrangler deploy"
	@echo "  make edge-tail      - wrangler tail"
	@echo "  make healer-run     - go run healer"
	@echo "  make healer-build   - build healer binary"
	@echo "  make healer-docker  - build healer image"
	@echo "  make compose-up     - docker compose up healer"
	@echo "  make compose-down   - docker compose down"
	@echo "  make ci             - typecheck edge + build/vet healer"

edge-install:
	cd edge-worker && npm ci

edge-setup:
	chmod +x scripts/setup-cloudflare.sh scripts/check-wrangler-kv.sh
	./scripts/setup-cloudflare.sh

check-kv:
	chmod +x scripts/check-wrangler-kv.sh
	./scripts/check-wrangler-kv.sh edge-worker/wrangler.toml

edge-dev:
	cd edge-worker && npx wrangler dev

edge-deploy:
	cd edge-worker && npx wrangler deploy

edge-tail:
	cd edge-worker && npx wrangler tail

healer-run:
	cd healer && go run .

healer-build:
	cd healer && go build -o autofix-healer .

healer-docker:
	docker build -t autofix-healer ./healer

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

ci:
	cd edge-worker && npm ci && npx tsc --noEmit
	cd healer && go mod download && go build -o autofix-healer . && go vet ./... && go test ./...
