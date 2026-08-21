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

# The Lua oracle is a SECOND bench, and separate on purpose: the main image
# reproduces production (nginx 1.20.1, Oracle Linux 9) and has no
# lua-nginx-module -- it refuses `content_by_lua_block` as an unknown
# directive. See test/bench/Dockerfile.openresty for why it is not merged into
# the first one. Nothing here touches the targets above.
BENCH_LUA_IMG := ngx-bench-lua
BENCH_LUA_CT  := ngx-bench-lua

.PHONY: bench-up bench-down bench-smoke bench-logs bench-shell
.PHONY: bench-lua-up bench-lua-down bench-lua-smoke bench-lua-logs bench-lua-shell

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

# No port is published: the only thing anyone asks of this container is
# `docker exec ... openresty -t`, over a file written into its /tmp.
bench-lua-up:
	@echo "bench-lua: building the image..."
	@docker build -f $(BENCH_DIR)/Dockerfile.openresty -t $(BENCH_LUA_IMG) $(BENCH_DIR)
	@docker rm -f $(BENCH_LUA_CT) >/dev/null 2>&1 || true
	@docker run -d --name $(BENCH_LUA_CT) $(BENCH_LUA_IMG) >/dev/null
	@echo "bench-lua: waiting for the container..."
	@for i in $$(seq 1 30); do \
		if docker exec $(BENCH_LUA_CT) openresty -v >/dev/null 2>&1; then \
			echo "bench-lua: up ($$(docker exec $(BENCH_LUA_CT) openresty -v 2>&1))"; \
			exit 0; \
		fi; \
		sleep 1; \
	done; \
	echo "bench-lua: container did not answer; see 'make bench-lua-logs'" >&2; \
	exit 1

bench-lua-smoke:
	@$(BENCH_DIR)/smoke-lua.sh $(BENCH_LUA_CT)

bench-lua-down:
	@docker rm -f $(BENCH_LUA_CT) >/dev/null 2>&1 || true
	@docker rmi -f $(BENCH_LUA_IMG) >/dev/null 2>&1 || true
	@echo "bench-lua: container and image removed"

bench-lua-logs:
	@docker logs $(BENCH_LUA_CT)

bench-lua-shell:
	@docker exec -it $(BENCH_LUA_CT) sh

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
	@echo "  bench-lua-up     brings up the Lua oracle (OpenResty container)"
	@echo "  bench-lua-smoke  proves the properties of the Lua oracle"
	@echo "  bench-lua-down   tears down and cleans the Lua oracle"
	@echo "  clean         removes bin/ and coverage artifacts"

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -o bin/ngx ./cmd/ngx

test:
	go test ./...

test-race:
	go test ./... -race

FUZZTIME ?= 60s
# Both targets, because CI runs both and a local `make fuzz` that ran one was
# a check that looked like the CI check and was not. The three ask different
# questions and fail on different defects: FuzzTokenizeSpans is the differential
# against crossplane's lexer, FuzzAlignment is the token-to-tree matching, and
# FuzzReconstitution is whether the spans TILE the file with nothing meaningful
# left over -- the property every byte substitution in v0.2 depends on.
#
# FuzzPlanValidation lives in another package, which is why the CI matrix now
# carries a package field per target: a target added in one place and not the
# other runs locally and nowhere else, or in CI and never on a laptop.
#
# Adding a target here without adding it to the matrix in ci.yml leaves it
# running locally and nowhere else.
fuzz:
	go test ./internal/config/ -run '^$$' -fuzz FuzzTokenizeSpans -fuzztime $(FUZZTIME)
	go test ./internal/config/ -run '^$$' -fuzz FuzzAlignment -fuzztime $(FUZZTIME)
	go test ./internal/config/ -run '^$$' -fuzz FuzzReconstitution -fuzztime $(FUZZTIME)
	go test ./internal/plan/ -run '^$$' -fuzz FuzzPlanValidation -fuzztime $(FUZZTIME)

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
