.PHONY: help edge-install edge-dev edge-deploy edge-tail edge-setup healer-run healer-build healer-docker healer-test healer-bench healer-bench-cpu healer-bench-mem healer-benchstat compose-up compose-down ci check-kv

help:
	@echo "AutoFix targets:"
	@echo "  make edge-install      - expand lockfile + npm ci"
	@echo "  make edge-setup        - create KV + queues and patch wrangler.toml"
	@echo "  make check-kv          - validate KV ids in wrangler.toml"
	@echo "  make edge-dev          - wrangler dev"
	@echo "  make edge-deploy       - wrangler deploy"
	@echo "  make edge-tail         - wrangler tail"
	@echo "  make healer-run        - go run healer"
	@echo "  make healer-build      - build healer binary"
	@echo "  make healer-docker     - build healer image"
	@echo "  make healer-test       - go test + coverage"
	@echo "  make healer-bench      - go test -bench (CPU)"
	@echo "  make healer-bench-cpu  - CPU profile (cpu.prof)"
	@echo "  make healer-bench-mem  - memory profile (mem.prof)"
	@echo "  make healer-benchstat  - benchstat old.txt new.txt (installs tool if needed)"
	@echo "  make compose-up        - docker compose up healer"
	@echo "  make compose-down      - docker compose down"
	@echo "  make ci                - typecheck edge + build/vet/test healer"

edge-install:
	chmod +x scripts/expand-lockfile.sh
	./scripts/expand-lockfile.sh
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

healer-test:
	cd healer && go test -coverprofile=coverage.out -covermode=atomic ./... && go tool cover -func=coverage.out

# Default bench: 3 runs, 1s each, allocations reported
healer-bench:
	cd healer && go test -run=^$$ -bench=. -benchmem -count=3 -benchtime=1s ./... | tee bench.txt

healer-bench-cpu:
	cd healer && go test -run=^$$ -bench=. -benchmem -cpuprofile=cpu.prof -count=1 -benchtime=2s ./...
	@echo "Inspect: go tool pprof healer/cpu.prof"

healer-bench-mem:
	cd healer && go test -run=^$$ -bench=. -benchmem -memprofile=mem.prof -count=1 -benchtime=2s ./...
	@echo "Inspect: go tool pprof healer/mem.prof"

# Compare two bench outputs: make healer-benchstat OLD=old.txt NEW=new.txt
healer-benchstat:
	@command -v benchstat >/dev/null 2>&1 || go install golang.org/x/perf/cmd/benchstat@latest
	@test -n "$(OLD)" -a -n "$(NEW)" || (echo "Usage: make healer-benchstat OLD=old.txt NEW=new.txt" && exit 1)
	benchstat $(OLD) $(NEW)

compose-up:
	docker compose up -d --build

compose-down:
	docker compose down

ci:
	chmod +x scripts/expand-lockfile.sh && ./scripts/expand-lockfile.sh
	cd edge-worker && npm ci && npx tsc --noEmit
	cd healer && go mod download && go build -o autofix-healer . && go vet ./... && go test -coverprofile=coverage.out -covermode=atomic ./... && go test -run=^$$ -bench=. -benchmem -count=1 -benchtime=100ms ./...
