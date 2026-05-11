#!/usr/bin/env bash
# Cross-library SlowBurst benchmark: 50 tasks x 2s sleep
# Tests how each library scales workers under bursty slow-task loads.
# Libraries: ultrapool, fasthttp, ants, goroutines
#
# Usage: ./crossbench_slowburst.sh [rounds] [benchcount]
#   rounds:     number of rounds (default: 3)
#   benchcount: -benchtime=Nx — number of bursts per round (default: 3)

set -euo pipefail
export PATH="/usr/local/go/bin:$PATH"

ROUNDS="${1:-5}"
BENCHCOUNT="${2:-3}"
BENCHTIME="${BENCHCOUNT}x"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

if [[ -x /usr/local/bin/benchmark.test ]]; then
    BENCH_BIN="/usr/local/bin/benchmark.test"
    RESULTS_DIR="/results/SlowBurst"
else
    BENCH_BIN=""
    RESULTS_DIR="$SCRIPT_DIR/results/SlowBurst"
fi
rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR"

# (libname, benchfunc, subtest-suffix)
LIBS=(
    "ultrapool:BenchmarkUltrapoolSlowBurst:"
    "fasthttp:BenchmarkFasthttpSlowBurst:"
    "ants:BenchmarkAntsSlowBurst:"
    "goroutines:BenchmarkPlainGoSlowBurst:"
)

echo "=== Cross-library SlowBurst benchmark ==="
echo "    Rounds: $ROUNDS | Bursts/round: $BENCHCOUNT (= -benchtime=$BENCHTIME)"
echo "    Workload: 50 tasks x 2s sleep"
echo ""

# Clean previous results
for entry in "${LIBS[@]}"; do
    name="${entry%%:*}"
    rm -f "$RESULTS_DIR/$name.txt"
done

run_bench() {
    local benchfunc="$1"
    local suffix="$2"
    local pattern="^${benchfunc}${suffix}\$"
    if [[ -n "$BENCH_BIN" ]]; then
        "$BENCH_BIN" -test.bench="$pattern" \
            -test.benchtime="$BENCHTIME" -test.count=1 -test.run='^$' -test.timeout=600s 2>/dev/null \
            | grep "^Benchmark" || true
    else
        go test -bench="$pattern" \
            -benchtime="$BENCHTIME" -count=1 -run='^$' -timeout=600s 2>/dev/null \
            | grep "^Benchmark" || true
    fi
}

for round in $(seq 1 "$ROUNDS"); do
    IFS=$'\n' read -r -d '' -a shuffled < <(printf '%s\n' "${LIBS[@]}" | sort -R && printf '\0') || true

    order=""
    for entry in "${shuffled[@]}"; do
        order="$order ${entry%%:*}"
    done
    echo "  Round $round/$ROUNDS: order =$order"

    for entry in "${shuffled[@]}"; do
        name="${entry%%:*}"
        rest="${entry#*:}"
        benchfunc="${rest%%:*}"
        suffix="${rest#*:}"

        result=$(run_bench "$benchfunc" "$suffix")
        if [[ -n "$result" ]]; then
            # Normalize benchmark name to a common prefix for benchstat-style stats
            normalized=$(echo "$result" | sed -E "s|^Benchmark[A-Za-z]+SlowBurst(/[A-Za-z]+)?-|BenchmarkSlowBurst-|")
            echo "$normalized" >> "$RESULTS_DIR/$name.txt"
            nsop=$(echo "$result" | awk '{print $3}')
            sec=$(awk -v n="$nsop" 'BEGIN { printf "%.2f", n / 1e9 }')
            peak=$(echo "$result" | grep -oE '[0-9.]+ peak-workers' | awk '{printf "%.0f", $1}' || true)
            burst=$(echo "$result" | grep -oE '[0-9.]+ burst-size' | awk '{printf "%.0f", $1}' || echo "50")
            tps=$(awk -v b="$burst" -v s="$sec" 'BEGIN { if (s > 0) printf "%.0f", b / s; else print "n/a" }')
            printf "    %-12s %s ns/burst (%ss)  %s tasks/sec  %s peak-workers\n" \
                "$name" "$nsop" "$sec" "$tps" "${peak:-n/a}"
        else
            printf "    %-12s no output\n" "$name"
        fi
    done
done
echo ""

echo "=== Summary ==="
echo ""
printf "%-12s %18s %12s %14s %3s  %s\n" "Library" "ns/burst" "sec/burst" "peak-workers" "n" "vs ultrapool"
printf "%-12s %18s %12s %14s %3s  %s\n" "-------" "--------" "---------" "------------" "-" "------------"

up_sec_mean=""
if [[ -f "$RESULTS_DIR/ultrapool.txt" ]]; then
    up_sec_mean=$(awk '{sum+=$3; n++} END {if (n>0) printf "%.4f", sum/n/1e9}' "$RESULTS_DIR/ultrapool.txt")
fi

for entry in "${LIBS[@]}"; do
    name="${entry%%:*}"
    if [[ -f "$RESULTS_DIR/$name.txt" ]]; then
        ns_stats=$(awk '{ sum+=$3; sumsq+=$3*$3; n++ } END {
            mean=sum/n
            sd=(n>1) ? sqrt((sumsq - sum*sum/n)/(n-1)) : 0
            cv=sd/mean*100
            printf "%10.0f ±%4.1f%%", mean, cv
        }' "$RESULTS_DIR/$name.txt")

        sec_mean=$(awk '{sum+=$3; n++} END {if (n>0) printf "%.3f", sum/n/1e9}' "$RESULTS_DIR/$name.txt")
        count=$(wc -l < "$RESULTS_DIR/$name.txt" | tr -d ' ')

        peak_stats=$(grep -oE '[0-9.]+ peak-workers' "$RESULTS_DIR/$name.txt" | awk '{
            sum+=$1; sumsq+=$1*$1; n++
        } END {
            if (n==0) { printf "%8s", "n/a"; exit }
            mean=sum/n
            sd=(n>1) ? sqrt((sumsq - sum*sum/n)/(n-1)) : 0
            cv=sd/mean*100
            printf "%6.0f ±%4.1f%%", mean, cv
        }')

        if [[ "$name" == "ultrapool" ]]; then
            printf "%-12s %18s %12s %14s %3s  %s\n" "$name" "$ns_stats" "${sec_mean}s" "$peak_stats" "$count" "(baseline)"
        elif [[ -n "$up_sec_mean" && -n "$sec_mean" ]]; then
            pct=$(awk -v a="$sec_mean" -v b="$up_sec_mean" 'BEGIN {
                diff = (a/b - 1) * 100
                if (diff <= 0) printf "+%.1f%% faster", -diff
                else printf "%.1f%% slower", diff
            }')
            printf "%-12s %18s %12s %14s %3s  %s\n" "$name" "$ns_stats" "${sec_mean}s" "$peak_stats" "$count" "$pct"
        else
            printf "%-12s %18s %12s %14s %3s\n" "$name" "$ns_stats" "${sec_mean}s" "$peak_stats" "$count"
        fi
    fi
done
echo ""
