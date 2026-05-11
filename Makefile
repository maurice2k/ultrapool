GO_IMAGE ?= golang:1.26-bookworm
NOCONTAINER ?=

# GO_VERSIONS, when set, turns every target below into a multi-version matrix:
# each target reruns once per version with GO_IMAGE=golang:$v-bookworm and
# prints a header. Leave empty to run once against GO_IMAGE.
#   make test GO_VERSIONS="1.22 1.24 1.26"
GO_VERSIONS ?=

ifeq ($(NOCONTAINER),1)
DOCKER_RUN =
DOCKER_RUN_BENCH = cd benchmark &&
else
DOCKER_ARGS ?=
DOCKER_RUN = docker run --rm $(DOCKER_ARGS) -v $(CURDIR):/src -w /src $(GO_IMAGE)
DOCKER_RUN_BENCH = docker run --rm $(DOCKER_ARGS) -v $(CURDIR):/src -w /src/benchmark $(GO_IMAGE)
endif

BENCH_ROUNDS ?= 3
BENCH_BENCHTIME ?= 5s
BENCH_PARALLELISM ?= 100
BENCH_WORKLOADS ?=
BENCH_RUN_PREFIX ?=
BENCH_CONTAINER_SETUP ?=
BENCH ?=

.PHONY: help test test-race test-verbose test-short crossbench crossbench-quick bench

help:
	@echo "ultrapool - Makefile targets"
	@echo ""
	@echo "  Tests (run inside Docker with $(GO_IMAGE)):"
	@echo "    make test             Run tests"
	@echo "    make test-race        Run tests with race detector"
	@echo "    make test-verbose     Run tests verbose with race detector"
	@echo "    make test-short       Run tests in short mode"
	@echo ""
	@echo "  Benchmarks (run inside Docker with $(GO_IMAGE)):"
	@echo "    make crossbench       Interleaved cross-library benchmark"
	@echo "    make crossbench-quick Quick cross-library sweep"
	@echo "    make bench BENCH=...  Run a specific benchmark"
	@echo ""
	@echo "  Any target above accepts GO_VERSIONS to fan out over multiple"
	@echo "  Go versions, with a header per version and a pass/fail summary:"
	@echo '    make test GO_VERSIONS="1.22 1.24 1.26"'
	@echo '    make crossbench-quick GO_VERSIONS="1.24 1.26"'
	@echo ""
	@echo "  Options:"
	@echo "    NOCONTAINER=1          Run locally without Docker"
	@echo "    GO_IMAGE=...           Docker image (default: $(GO_IMAGE))"
	@echo "    GO_VERSIONS='1.x ...'  Fan out target over Go versions (default: single run with GO_IMAGE)"
	@echo "    BENCH_ROUNDS=N         Rounds (default: 3)"
	@echo "    BENCH_BENCHTIME=Xs     Duration per run (default: 5s)"
	@echo "    BENCH_PARALLELISM=N    Goroutine parallelism (default: 100; crossbench: 1, 10, 50, or 100)"
	@echo "    BENCH_WORKLOADS='...'  Space-separated crossbench workloads to run"
	@echo "    BENCH_RUN_PREFIX='...' Prefix each crossbench go test, e.g. numactl --cpunodebind=0"
	@echo "    BENCH_CONTAINER_SETUP='...' Setup command run before Docker crossbench"
	@echo "    DOCKER_ARGS='...'      Extra docker run args, e.g. cpuset or env vars"
	@echo ""
	@echo "  Examples:"
	@echo '    make test-race'
	@echo '    make crossbench BENCH_ROUNDS=5 BENCH_BENCHTIME=5s'
	@echo '    make crossbench BENCH_WORKLOADS="AES_CBC_8kB" BENCH_RUN_PREFIX="numactl --cpunodebind=0 --membind=0" DOCKER_ARGS="--cap-add SYS_NICE --cpuset-cpus=0-23,48-71 --cpuset-mems=0 -e GOMAXPROCS=48" BENCH_CONTAINER_SETUP="apt-get update >/dev/null && apt-get install -y numactl >/dev/null &&"'
	@echo '    make test-race GO_VERSIONS="1.22 1.24 1.26"'
	@echo '    make bench BENCH='"'"'BenchmarkUltrapoolWorkerpool/(AES_CBC_8kB)/100$$'"'"''
	@echo '    make bench BENCH='"'"'BenchmarkUltrapoolWorkerpool/(AES_CBC_8kB)/100$$'"'"' BENCH_ROUNDS=5 BENCH_BENCHTIME=5s'

# ---------------------------------------------------------------------------
# Multi-version dispatcher.
#
# When GO_VERSIONS is non-empty, the target loops over each version,
# recursively invoking itself with GO_IMAGE swapped and GO_VERSIONS cleared
# (the latter breaks recursion so the inner call hits the real recipe).
# Per-version failures are logged but do not abort the loop; a summary at
# the end lists passing/failing versions and propagates a non-zero exit.
# ---------------------------------------------------------------------------

define run_versions
	@pass=""; fail=""; \
	for v in $(GO_VERSIONS); do \
		image="golang:$$v-bookworm"; \
		echo ""; \
		echo "============================================================"; \
		echo "===== Go $$v ($$image): $(1)"; \
		echo "============================================================"; \
		if $(MAKE) --no-print-directory $(1) GO_IMAGE=$$image GO_VERSIONS=; then \
			pass="$$pass $$v"; \
		else \
			echo ">>> FAILED on Go $$v"; \
			fail="$$fail $$v"; \
		fi; \
	done; \
	echo ""; \
	echo "============================================================"; \
	echo "===== Summary: $(1)"; \
	echo "============================================================"; \
	echo "  passed:$$pass"; \
	echo "  failed:$$fail"; \
	[ -z "$$fail" ]
endef

test:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,test)
else
	$(DOCKER_RUN) go test -timeout 60s ./...
endif

test-race:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,test-race)
else
	$(DOCKER_RUN) go test -race -timeout 120s ./...
endif

test-verbose:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,test-verbose)
else
	$(DOCKER_RUN) go test -v -race -timeout 120s ./...
endif

test-short:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,test-short)
else
	$(DOCKER_RUN) go test -short -timeout 30s ./...
endif

crossbench:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,crossbench)
else ifeq ($(NOCONTAINER),1)
	cd benchmark && BENCH_WORKLOADS='$(BENCH_WORKLOADS)' BENCH_RUN_PREFIX='$(BENCH_RUN_PREFIX)' ./crossbench.sh $(BENCH_ROUNDS) $(BENCH_BENCHTIME) $(BENCH_PARALLELISM)
else
	$(DOCKER_RUN_BENCH) bash -c '$(BENCH_CONTAINER_SETUP) go install golang.org/x/perf/cmd/benchstat@latest && BENCH_WORKLOADS='"'"'$(BENCH_WORKLOADS)'"'"' BENCH_RUN_PREFIX='"'"'$(BENCH_RUN_PREFIX)'"'"' ./crossbench.sh $(BENCH_ROUNDS) $(BENCH_BENCHTIME) $(BENCH_PARALLELISM)'
endif

crossbench-quick:
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,crossbench-quick)
else ifeq ($(NOCONTAINER),1)
	cd benchmark && ./crossbench_quick.sh $(BENCH_ROUNDS) $(BENCH_BENCHTIME) $(BENCH_PARALLELISM)
else
	$(DOCKER_RUN_BENCH) bash -c 'go install golang.org/x/perf/cmd/benchstat@latest && ./crossbench_quick.sh $(BENCH_ROUNDS) $(BENCH_BENCHTIME) $(BENCH_PARALLELISM)'
endif

bench:
ifndef BENCH
	$(error BENCH is required, e.g. make bench BENCH='BenchmarkUltrapoolWorkerpool/(AES_CBC_8kB)/100$$')
endif
ifneq ($(strip $(GO_VERSIONS)),)
	$(call run_versions,bench)
else ifeq ($(NOCONTAINER),1)
	cd benchmark && go test -bench='$(BENCH)' -benchtime=$(BENCH_BENCHTIME) -count=$(BENCH_ROUNDS) -run='^$$' -v
else
	$(DOCKER_RUN_BENCH) go test -bench='$(BENCH)' -benchtime=$(BENCH_BENCHTIME) -count=$(BENCH_ROUNDS) -run='^$$' -v
endif
