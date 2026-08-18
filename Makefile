# ngx targets.
#
# The test bench (test/bench) is a disposable container with sshd and
# nginx, used by the integration tests of the remote path.

SHELL := /bin/bash

BENCH_DIR   := test/bench
BENCH_IMG   := ngx-bench
BENCH_CT    := ngx-bench
# High, fixed port on the host: the integration tests know where to connect.
BENCH_PORT ?= 2222
BENCH_KEY := $(BENCH_DIR)/.key/id_ed25519

.PHONY: bench-up bench-down bench-smoke bench-logs bench-shell

# The test key is generated, never committed.
$(BENCH_KEY):
	@mkdir -p $(dir $(BENCH_KEY))
	@ssh-keygen -t ed25519 -N '' -C ngx-bench -f $(BENCH_KEY) >/dev/null
	@echo "bench: test key generated at $(BENCH_KEY)"

bench-up: $(BENCH_KEY)
	@echo "bench: building the image..."
	@docker build -t $(BENCH_IMG) $(BENCH_DIR)
	@docker rm -f $(BENCH_CT) >/dev/null 2>&1 || true
	@docker create --name $(BENCH_CT) \
		-p 127.0.0.1:$(BENCH_PORT):22 \
		$(BENCH_IMG) >/dev/null
	@docker cp $(BENCH_KEY).pub $(BENCH_CT):/public-key.pub
	@docker start $(BENCH_CT) >/dev/null
	@echo "bench: waiting for sshd..."
	@for i in $$(seq 1 60); do \
		if ssh -i $(BENCH_KEY) -p $(BENCH_PORT) \
			-o IdentitiesOnly=yes -o BatchMode=yes \
			-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
			-o LogLevel=ERROR -o ConnectTimeout=3 \
			ngxtest@127.0.0.1 true 2>/dev/null; then \
			echo "bench: up at 127.0.0.1:$(BENCH_PORT) (user ngxtest)"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "bench: sshd did not answer; see 'make bench-logs'" >&2; \
	exit 1

bench-smoke:
	@$(BENCH_DIR)/smoke.sh $(BENCH_PORT) $(BENCH_KEY)

bench-down:
	@docker rm -f $(BENCH_CT) >/dev/null 2>&1 || true
	@docker rmi -f $(BENCH_IMG) >/dev/null 2>&1 || true
	@rm -rf $(dir $(BENCH_KEY))
	@echo "bench: container, image and test key removed"

bench-logs:
	@docker logs $(BENCH_CT)

bench-shell:
	@ssh -i $(BENCH_KEY) -p $(BENCH_PORT) \
		-o IdentitiesOnly=yes \
		-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null \
		-o LogLevel=ERROR ngxtest@127.0.0.1

.PHONY: help build test test-race fuzz cover fmt lint verify clean

# `make` with no argument lists what can be done, instead of running
# something by mistake.
.DEFAULT_GOAL := help

help:
	@echo "ngx targets:"
	@echo "  build         compiles the binary into bin/ngx"
	@echo "  test          tests, without -race (fast)"
	@echo "  test-race     tests with the race detector"
	@echo "  fuzz          fuzz of the alignment for FUZZTIME (default 60s)"
	@echo "  cover         coverage in cover.html"
	@echo "  fmt           applies gofmt"
	@echo "  lint          gofmt -l and go vet, without changing any file"
	@echo "  verify        lint + test-race + short fuzz; what CI runs"
	@echo "  bench-up      brings up the test container (sshd + nginx)"
	@echo "  bench-smoke   proves the properties of the bench"
	@echo "  bench-down    tears down and cleans the bench"
	@echo "  clean         removes bin/ and coverage artifacts"

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -o bin/ngx ./cmd/ngx

test:
	go test ./...

test-race:
	go test ./... -race

FUZZTIME ?= 60s
fuzz:
	go test ./internal/config/ -run '^$$' -fuzz FuzzAlignment -fuzztime $(FUZZTIME)

cover:
	go test ./... -coverprofile=cover.out
	go tool cover -html=cover.out -o cover.html
	@echo "coverage in cover.html"

fmt:
	gofmt -w .

# lint changes no file: it serves CI and a check before committing.
lint:
	@test -z "$$(gofmt -l . | tee /dev/stderr)" || (echo "files above are not gofmt-clean" >&2; exit 1)
	go vet ./...

verify: lint test-race
	@$(MAKE) fuzz FUZZTIME=30s

clean:
	rm -rf bin cover.out cover.html
