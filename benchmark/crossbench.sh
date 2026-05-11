#!/usr/bin/env bash
# Cross-library interleaved benchmark
# Runs one iteration per library in randomized order to share thermal state
# across all libraries equally. Repeats multiple rounds for statistical power.
#
# Usage: ./crossbench.sh [rounds] [benchtime] [parallelism]
#   rounds:      number of rounds (default: 10)
#   benchtime:   benchmark duration per run (default: 1s)
#   parallelism: goroutine parallelism level (default: 100)
#   BENCH_WORKLOADS: optional space-separated workload filter, e.g. AES_CBC_8kB
#   BENCH_RUN_PREFIX: optional command prefix for each go test invocation
#
#   ./crossbench.sh --summary [parallelism]
#       Re-emit the summary from already-collected results without re-running
#       benchmarks. Useful to recover output if a capture got truncated.

set -euo pipefail
export PATH="/usr/local/go/bin:$PATH"

SUMMARY_ONLY=0
if [[ "${1:-}" == "--summary" || "${1:-}" == "--summary-only" ]]; then
    SUMMARY_ONLY=1
    PARALLELISM="${2:-100}"
    ROUNDS=""
    BENCHTIME=""
else
    ROUNDS="${1:-5}"
    BENCHTIME="${2:-3s}"
    PARALLELISM="${3:-100}"
fi

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Detect if running inside container (pre-compiled binary available)
if [[ -x /usr/local/bin/benchmark.test ]]; then
    BENCH_BIN="/usr/local/bin/benchmark.test"
    RESULTS_DIR="/results"
else
    BENCH_BIN=""
    RESULTS_DIR="$SCRIPT_DIR/results"
fi
if [[ "$SUMMARY_ONLY" -eq 0 ]]; then
    rm -rf "$RESULTS_DIR"
    mkdir -p "$RESULTS_DIR"
elif [[ ! -d "$RESULTS_DIR" ]]; then
    echo "ERROR: --summary requested but $RESULTS_DIR does not exist." >&2
    exit 1
fi

# Define libraries and their benchmark function names
LIBS=(
    "ultrapool:BenchmarkUltrapoolWorkerpool"
    "ultrapool-v1:BenchmarkUltrapoolV1Workerpool"
    "fasthttp:BenchmarkFasthttpWorkerpool"
    "ants:BenchmarkAntsWorkerpool"
    "pond:BenchmarkPondWorkerpool"
    "gammazero:BenchmarkGammazeroWorkerpool"
    "goroutines:BenchmarkPlainGoRoutines"
)

# Define workloads to test
WORKLOADS=(
    "Sleep_1ms"
    "SHA256_1kB"
    "AES_CBC_1kB"
    "AES_CBC_8kB"
    "CRC32_64B"
    "MemScan_4kB"
    "Mixed_Bimodal"
    "Mutex_Contention"
)

if [[ -n "${BENCH_WORKLOADS:-}" ]]; then
    read -r -a WORKLOADS <<< "$BENCH_WORKLOADS"
fi

RUN_PREFIX=()
if [[ -n "${BENCH_RUN_PREFIX:-}" ]]; then
    read -r -a RUN_PREFIX <<< "$BENCH_RUN_PREFIX"
fi

detect_cpu_model() {
    local model

    model=$(lscpu 2>/dev/null | awk -F: '/^Model name:/ {sub(/^[ \t]+/, "", $2); print $2; exit}')
    if [[ -n "$model" ]]; then
        echo "$model"
        return
    fi

    model=$(grep -m1 '^model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | xargs || true)
    if [[ -n "$model" ]]; then
        echo "$model"
        return
    fi

    # AWS Graviton ARM hosts expose implementer/part but not model name.
    local vendor part
    vendor=$(lscpu 2>/dev/null | awk -F: '/^Vendor ID:/ {sub(/^[ \t]+/, "", $2); print $2; exit}')
    part=$(grep -m1 '^CPU part' /proc/cpuinfo 2>/dev/null | cut -d: -f2- | xargs || true)
    if [[ "$vendor" == "ARM" && "$part" == "0xd4f" ]]; then
        echo "AWS Graviton4"
        return
    fi

    model=$(sysctl -n machdep.cpu.brand_string 2>/dev/null || true)
    if [[ -n "$model" ]]; then
        echo "$model"
        return
    fi

    echo "unknown"
}

CPU_MODEL=$(detect_cpu_model)
CPU_CORES=$(nproc 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo "?")

if [[ "$SUMMARY_ONLY" -eq 1 ]]; then
    echo "=== Re-emitting summary from $RESULTS_DIR (parallelism=$PARALLELISM) ==="
    echo "    CPU: $CPU_MODEL ($CPU_CORES cores)"
    echo ""
    # Skip the benchmark phase entirely.
    SKIP_BENCH=1
else
    SKIP_BENCH=0
    echo "=== Cross-library interleaved benchmark ==="
    echo "    CPU: $CPU_MODEL ($CPU_CORES cores)"
    echo "    Rounds: $ROUNDS | Benchtime: $BENCHTIME | Parallelism: $PARALLELISM"
    echo "    Libraries: ${#LIBS[@]} | Workloads: ${#WORKLOADS[@]}"
    echo "    Workloads: ${WORKLOADS[*]}"
    if [[ "${#RUN_PREFIX[@]}" -gt 0 ]]; then
        echo "    Run prefix: ${RUN_PREFIX[*]}"
    fi
    echo ""
fi

if [[ "$SKIP_BENCH" -eq 0 ]]; then
for workload in "${WORKLOADS[@]}"; do
    FILTER="${workload}/${PARALLELISM}\$"
    WL_DIR="$RESULTS_DIR/$workload"
    mkdir -p "$WL_DIR"

    # Clean previous results for this workload
    for entry in "${LIBS[@]}"; do
        name="${entry%%:*}"
        rm -f "$WL_DIR/$name.txt"
    done

    echo "--- Workload: $workload (parallelism=$PARALLELISM) ---"
    echo ""

    for round in $(seq 1 "$ROUNDS"); do
        # Shuffle library order each round
        IFS=$'\n' read -r -d '' -a shuffled < <(printf '%s\n' "${LIBS[@]}" | sort -R && printf '\0') || true

        order=""
        for entry in "${shuffled[@]}"; do
            name="${entry%%:*}"
            order="$order $name"
        done
        echo "  Round $round/$ROUNDS: order =$order"

        for entry in "${shuffled[@]}"; do
            name="${entry%%:*}"
            benchfunc="${entry#*:}"

            # Run single iteration, normalize benchmark name for benchstat comparability
            if [[ -n "$BENCH_BIN" ]]; then
                result=$("${RUN_PREFIX[@]}" "$BENCH_BIN" -test.bench="$benchfunc/$FILTER" \
                    -test.benchtime="$BENCHTIME" -test.count=1 -test.run='^$' -test.timeout=60s 2>/dev/null \
                    | grep "^Benchmark" | sed -E "s|^Benchmark[A-Za-z]+/|BenchmarkWorkerpool/|" || true)
            else
                result=$("${RUN_PREFIX[@]}" go test -bench="$benchfunc/$FILTER" \
                    -benchtime="$BENCHTIME" -count=1 -run='^$' -timeout=60s 2>/dev/null \
                    | grep "^Benchmark" | sed -E "s|^Benchmark[A-Za-z]+/|BenchmarkWorkerpool/|" || true)
            fi

            if [[ -n "$result" ]]; then
                echo "$result" >> "$WL_DIR/$name.txt"
                nsop=$(echo "$result" | awk '{print $3}')
                opsec=$(echo "$result" | grep -oE '[0-9.]+ ops/sec' | awk '{printf "%.0f", $1}')
                peak=$(echo "$result" | grep -oE '[0-9.]+ peak-workers' | awk '{printf "%.0f", $1}' || true)
                printf "    %-12s %s ns/op  %s ops/sec  %s peak-workers\n" "$name" "$nsop" "$opsec" "${peak:-n/a}"
            fi
        done
    done
    echo ""
done
fi  # end SKIP_BENCH guard

echo "=== Summary ==="
echo ""

for workload in "${WORKLOADS[@]}"; do
    WL_DIR="$RESULTS_DIR/$workload"

    echo "--- $workload (parallelism=$PARALLELISM) ---"
    printf "%-12s %15s %14s %14s %3s  %s\n" "Library" "ns/op" "ops/sec" "peak-workers" "n" "vs ultrapool"
    printf "%-12s %15s %14s %14s %3s  %s\n" "-------" "-----" "-------" "------------" "-" "------------"

    # Get ultrapool mean for comparison
    up_opsec_mean=""
    if [[ -f "$WL_DIR/ultrapool.txt" ]]; then
        up_opsec_mean=$(grep -oE '[0-9.]+ ops/sec' "$WL_DIR/ultrapool.txt" | awk '{sum+=$1; n++} END {if(n>0) printf "%.2f", sum/n; else print ""}')
    fi

    for entry in "${LIBS[@]}"; do
        name="${entry%%:*}"
        if [[ -f "$WL_DIR/$name.txt" ]]; then
            # Compute mean ± CV% for ns/op (field $3) with fixed-width formatting
            nsop_stats=$(awk '{
                sum+=$3; sumsq+=$3*$3; n++
            } END {
                mean=sum/n
                if (n>1) sd=sqrt((sumsq - sum*sum/n)/(n-1)); else sd=0
                cv=sd/mean*100
                printf "%7.1f ±%4.1f%%", mean, cv
            }' "$WL_DIR/$name.txt")

            count=$(wc -l < "$WL_DIR/$name.txt" | tr -d ' ')

            # Compute mean ops/sec from the ops/sec field in the benchmark output
            opsec_stats=$(grep -oE '[0-9.]+ ops/sec' "$WL_DIR/$name.txt" | awk '{
                sum+=$1; sumsq+=$1*$1; n++
            } END {
                if (n==0) { printf "%10s", "n/a"; exit }
                mean=sum/n
                if (n>1) sd=sqrt((sumsq - sum*sum/n)/(n-1)); else sd=0
                cv=sd/mean*100
                printf "%8.0f ±%4.1f%%", mean, cv
            }')

            # Compute mean ops/sec for comparison
            opsec_mean=$(grep -oE '[0-9.]+ ops/sec' "$WL_DIR/$name.txt" | awk '{sum+=$1; n++} END {if(n>0) printf "%.2f", sum/n; else print ""}')

            # Compute mean peak-workers
            peak_stats=$(grep -oE '[0-9.]+ peak-workers' "$WL_DIR/$name.txt" | awk '{
                sum+=$1; sumsq+=$1*$1; n++
            } END {
                if (n==0) { printf "%8s", "n/a"; exit }
                mean=sum/n
                if (n>1) sd=sqrt((sumsq - sum*sum/n)/(n-1)); else sd=0
                cv=sd/mean*100
                printf "%6.0f ±%4.1f%%", mean, cv
            }' || true)

            if [[ "$name" == "ultrapool" ]]; then
                printf "%-12s %15s %14s %14s %3s  %s\n" "$name" "$nsop_stats" "$opsec_stats" "$peak_stats" "$count" "(baseline)"
            elif [[ -n "$up_opsec_mean" && -n "$opsec_mean" ]]; then
                pct=$(awk -v a="$opsec_mean" -v b="$up_opsec_mean" 'BEGIN {
                    diff = (a/b - 1) * 100
                    if (diff >= 0) printf "+%.1f%% faster", diff
                    else printf "%.1f%% slower", diff
                }')
                printf "%-12s %15s %14s %14s %3s  %s\n" "$name" "$nsop_stats" "$opsec_stats" "$peak_stats" "$count" "$pct"
            else
                printf "%-12s %15s %14s %14s %3s\n" "$name" "$nsop_stats" "$opsec_stats" "$peak_stats" "$count"
            fi
        fi
    done
    echo ""
done
