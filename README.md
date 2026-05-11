# ultrapool

**The fastest worker pool in Go — by a wide margin.**

*ultrapool* is a generic, sharded, adaptive worker pool for Go. Tasks are dispatched in **hundreds of nanoseconds**, workers spawn under backpressure and retire when idle, and the whole thing scales cleanly from a laptop to a 96-core server.

We tested *ultrapool* v2 head-to-head against every popular worker pool in the ecosystem — `ants`, `pond`, `gammazero/workerpool`, `fasthttp/workerpool` — and against raw goroutines, across four different CPUs and eight different workloads. External worker-pool libraries do not win a single listed workload. Raw goroutines do not win one either. On most workloads it isn't close: often 2–12× the throughput of the next best library, and consistently faster than spawning goroutines directly.

It is also dramatically faster than ultrapool v1 — up to **3× the throughput** on contended workloads on a 48-core EPYC, with the same simple API.


## Highlights

- ⚡ **Sub-microsecond dispatch** — 120–406 ns/op across all our test machines for short tasks.
- 🚀 **Beats raw goroutines** on every workload we tested — including a 96-core box where goroutines normally win.
- 🧵 **Generic API** (`WorkerPool[T]`) — no `interface{}` casts, no boxing.
- 🔪 **Sharded scheduling** with per-shard CAS-driven worker spawning — contention scales with cores, not against them.
- 🌱 **Adaptive sizing** — workers spawn on visible backlog, retire after an idle timeout, with both per-shard and global caps.
- 🛡️ **Graceful shutdown** — `Stop`, `StopAndWait`, `StopWithTimeout`; in-flight tasks always finish.
- 🪶 **Tiny** — single file, no dependencies, Go 1.20+.


## Install

```sh
go get github.com/maurice2k/ultrapool/v2
```


## Example

```go
wp := ultrapool.NewWorkerPool(func(conn net.Conn) {
    serveConn(conn)
})

wp.Start()
defer wp.Stop()

// submit work — typed, no casts
wp.AddTask(conn)

// back-pressure aware variant; blocks if the pool is saturated
wp.AddTaskWithBlocking(conn)
```

For graceful shutdown that waits for in-flight tasks:

```go
wp.StopAndWait()                  // waits indefinitely
wp.StopWithTimeout(5 * time.Second) // returns false on timeout
```


## Architecture

*ultrapool* originally drew inspiration from the worker pool in [valyala/fasthttp](https://github.com/valyala/fasthttp/blob/master/workerpool.go), but v2 has been redesigned from the ground up for high-core-count machines.

- **Sharded queues.** The pool is split into N shards (default: `GOMAXPROCS/2`, clamped to `[2, 48]`). Each shard owns its own buffered task channel and its own worker set. Dispatchers pick a random shard, so contention is spread across cores instead of funneling through a single hot lock or channel.
- **Adaptive, lock-free spawning.** Each shard tracks its worker count atomically. When a dispatcher sees a non-empty queue, it tries to spawn another worker via CAS-reserved slots, respecting both per-shard and global caps. No mutexes on the hot path.
- **Idle retirement.** Workers exit after `idleWorkerLifetime` (default 1s) once the shard is above its floor, so a quiet pool collapses back to a small footprint instead of holding thousands of goroutines hostage.
- **Race-free shutdown.** A per-shard `RWMutex` fences sends against `Stop()`'s channel close. Late dispatchers see the stopped flag under the lock and bail with `ErrPoolStopped` — no panics on closed channels, ever.
- **Generics end-to-end.** The task type is a type parameter, so there's no `interface{}` boxing in the dispatch path and no type assertions in user code.

The end result: *ultrapool* spends almost all its CPU time doing your work, not coordinating workers.


## Benchmarks

All benchmarks are run with the cross-library harness in [`benchmark/`](benchmark/), which **interleaves** the runs of every library in random order across multiple rounds. This avoids the usual warm-cache / TLB / scheduler-affinity bias that one-library-at-a-time benchmarks suffer from.

- **Parallelism:** 100 concurrent goroutines submitting tasks
- **Benchtime:** 5 seconds per run
- **Workloads:** sleep, SHA256, AES-CBC (1 kB & 8 kB), CRC32, memory scan, mixed bimodal, mutex contention
- **Tools:** Docker `golang:1.26-bookworm`, `go test -bench`; mean ± stddev across rounds

Legend: 🟢 fastest in row.

### Apple M2 Max (12 cores, arm64)

| Workload | ultrapool **v2** | ultrapool v1 | fasthttp | ants | pond | gammazero | goroutines |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sleep 1µs            | 🟢 **335 ns** | 438 ns (−24%)  | 1626 ns (−79%)| 858 ns (−61%) | 2269 ns (−85%) | 907 ns (−63%)  | 1627 ns (−78%)|
| SHA256 1 kB          | 🟢 **327 ns** | 491 ns (−33%)  | 1675 ns (−80%)| 912 ns (−64%) | 2860 ns (−88%) | 1050 ns (−69%) | 1048 ns (−68%)|
| AES-CBC 1 kB         | 🟢 **1286 ns**| 3549 ns (−49%) | 3374 ns (−62%)| 3905 ns (−66%)| 4292 ns (−70%) | 5111 ns (−75%) | 8115 ns (−84%)|
| AES-CBC 8 kB         | 🟢 **3021 ns**| 6767 ns (−52%) | 6701 ns (−53%)| 7955 ns (−62%)| 7998 ns (−63%) |12382 ns (−76%) |13500 ns (−77%)|
| CRC32 64 B           | 🟢 **179 ns** | 415 ns (−57%)  | 1265 ns (−86%)| 846 ns (−79%) | 2925 ns (−94%) | 684 ns (−74%)  | 633 ns (−72%) |
| MemScan 4 kB         | 🟢 **468 ns** | 620 ns (−25%)  | 1780 ns (−74%)| 1003 ns (−54%)| 3422 ns (−86%) | 1526 ns (−70%) | 804 ns (−41%) |
| Mixed bimodal        | 🟢 **356 ns** | 885 ns (−60%)  | 1636 ns (−78%)| 1273 ns (−72%)| 3200 ns (−89%) | 1592 ns (−78%) | 1021 ns (−65%)|
| Mutex contention     | 🟢 **220 ns** | 440 ns (−50%)  | 1406 ns (−84%)| 867 ns (−75%) | 2862 ns (−92%) | 709 ns (−69%)  | 824 ns (−72%) |

ns/op (lower is better); deltas are vs. ultrapool v2.


### AWS Graviton4 (96 cores, arm64)

| Workload | ultrapool **v2** | ultrapool v1 | fasthttp | ants | pond | gammazero | goroutines |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sleep 1µs            | 🟢 **167 ns** | 183 ns (−9%)   | 756 ns (−78%) | 622 ns (−73%) | 1549 ns (−89%) | 1085 ns (−85%) | 324 ns (−47%) |
| SHA256 1 kB          | 🟢 **148 ns** | 180 ns (−18%)  | 712 ns (−79%) | 613 ns (−76%) | 1310 ns (−89%) | 1042 ns (−86%) | 209 ns (−29%) |
| AES-CBC 1 kB         | 🟢 **1006 ns**| 7887 ns (−84%) | 1462 ns (−31%)| 6591 ns (−85%)| 2322 ns (−56%) | 2623 ns (−62%) | 1277 ns (−21%)|
| AES-CBC 8 kB         | 🟢 **1857 ns**| 14356 ns (−86%) | 2801 ns (−33%)| 19127 ns (−90%)| 2778 ns (−33%) | 3010 ns (−38%) | 3307 ns (−44%)|
| CRC32 64 B           | 🟢 **154 ns** | 182 ns (−15%)  | 727 ns (−79%) | 575 ns (−73%) | 1556 ns (−90%) | 1040 ns (−85%) | 197 ns (−21%) |
| MemScan 4 kB         | 🟢 **160 ns** | 185 ns (−13%)  | 766 ns (−79%) | 627 ns (−74%) | 1655 ns (−90%) | 1030 ns (−84%) | 213 ns (−24%) |
| Mixed bimodal        | 🟢 **263 ns** | 434 ns (−39%)  | 993 ns (−74%) | 1654 ns (−84%)| 2030 ns (−87%) | 1727 ns (−85%) | 345 ns (−24%) |
| Mutex contention     | 🟢 **266 ns** | 1290 ns (−79%) | 726 ns (−63%) | 600 ns (−56%) | 1570 ns (−83%) | 1073 ns (−75%) | 606 ns (−56%) |


### AMD EPYC-Milan (48 cores, amd64)

| Workload | ultrapool **v2** | ultrapool v1 | fasthttp | ants | pond | gammazero | goroutines |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sleep 1µs            | 🟢 **125 ns** | 319 ns (−61%)  | 1701 ns (−93%)| 1434 ns (−91%)| 3704 ns (−97%) | 1932 ns (−94%) | 518 ns (−76%) |
| SHA256 1 kB          | 🟢 **121 ns** | 296 ns (−59%)  | 1664 ns (−93%)| 1253 ns (−90%)| 3192 ns (−96%) | 1965 ns (−94%) | 418 ns (−71%) |
| AES-CBC 1 kB         | 🟢 **1153 ns**| 1517 ns (−24%) | 2491 ns (−54%)| 8576 ns (−87%)| 3832 ns (−70%) | 4457 ns (−74%) | 1675 ns (−32%)|
| AES-CBC 8 kB         | 🟢 **1848 ns**| 5400 ns (−66%) | 4551 ns (−59%)| 29687 ns (−94%)| 6243 ns (−70%) | 4659 ns (−60%) | 6189 ns (−69%)|
| CRC32 64 B           | 🟢 **116 ns** | 308 ns (−62%)  | 1594 ns (−93%)| 1363 ns (−91%)| 4712 ns (−98%) | 1909 ns (−94%) | 403 ns (−71%) |
| MemScan 4 kB         | 🟢 **127 ns** | 269 ns (−53%)  | 1700 ns (−92%)| 1133 ns (−89%)| 5247 ns (−98%) | 1857 ns (−93%) | 397 ns (−68%) |
| Mixed bimodal        | 🟢 **267 ns** | 669 ns (−60%)  | 1675 ns (−84%)| 2810 ns (−91%)| 4408 ns (−94%) | 2638 ns (−90%) | 690 ns (−61%) |
| Mutex contention     | 🟢 **332 ns** | 580 ns (−43%)  | 1589 ns (−79%)| 1301 ns (−75%)| 4847 ns (−93%) | 1840 ns (−82%) | 680 ns (−49%) |


### Intel Xeon Platinum 8275CL (96 cores, amd64)

| Workload | ultrapool **v2** | ultrapool v1 | fasthttp | ants | pond | gammazero | goroutines |
|---|---:|---:|---:|---:|---:|---:|---:|
| Sleep 1µs            | 🟢 **406 ns** | 459 ns (−12%)  | 1303 ns (−69%)| 1391 ns (−71%)| 3358 ns (−88%) | 2023 ns (−80%) | 530 ns (−23%) |
| SHA256 1 kB          | 🟢 **311 ns** | 360 ns (−14%)  | 1465 ns (−79%)| 1425 ns (−78%)| 2588 ns (−88%) | 1993 ns (−84%) | 613 ns (−49%) |
| AES-CBC 1 kB         | 🟢 **1652 ns**| 1977 ns (−17%) | 3511 ns (−54%)| 17241 ns (−90%)| 4730 ns (−66%) | 6407 ns (−74%) | 1847 ns (−11%)|
| AES-CBC 8 kB         | 🟢 **3008 ns**| 7169 ns (−56%) | 5433 ns (−45%)| 65835 ns (−95%)| 9285 ns (−68%) | 11295 ns (−73%) | 5225 ns (−42%)|
| CRC32 64 B           | 🟢 **296 ns** | 347 ns (−15%)  | 1375 ns (−78%)| 1276 ns (−77%)| 3238 ns (−91%) | 1887 ns (−84%) | 332 ns (−11%) |
| MemScan 4 kB         | 🟢 **296 ns** | 354 ns (−16%)  | 1439 ns (−80%)| 1379 ns (−79%)| 3254 ns (−91%) | 1996 ns (−85%) | 353 ns (−16%) |
| Mixed bimodal        | 🟢 **596 ns** | 820 ns (−28%)  | 2080 ns (−72%)| 5360 ns (−89%)| 3697 ns (−84%) | 3116 ns (−81%) | 605 ns (−2%)  |
| Mutex contention     | 🟢 **599 ns** | 1327 ns (−54%) | 1408 ns (−58%)| 1330 ns (−55%)| 3227 ns (−81%) | 1986 ns (−70%) | 1285 ns (−52%)|


Raw logs used while preparing this release are generated files and intentionally
ignored by git. Locally they are available here:

- [benchmark-arm64-apple-m2-max-12core.txt](benchmark/results/benchmark-arm64-apple-m2-max-12core.txt)
- [benchmark-arm64-aws-graviton4-96core.txt](benchmark/results/benchmark-arm64-aws-graviton4-96core.txt)
- [benchmark-amd64-amd-epyc-48core.txt](benchmark/results/benchmark-amd64-amd-epyc-48core.txt)
- [benchmark-amd64-intel-xeon-platinum-8275cl-96core.txt](benchmark/results/benchmark-amd64-intel-xeon-platinum-8275cl-96core.txt)


### Why this matters

A few things to note from these tables:

- **ultrapool v2 outperforms raw `go` goroutines on every listed workload.** That is unusual: most worker pools exist to *recover* the overhead they themselves introduce, and lose to plain goroutines on light work. *ultrapool* doesn't.
- **External worker-pool libraries do not win a single row.** Across Apple M2 Max, AWS Graviton4, AMD EPYC, and Intel c5.metal, `ants`, `pond`, `gammazero`, and `fasthttp` all lose the table.
- **The gap widens with core count.** On the server-class EPYC and Graviton4 runs, *ultrapool* is often 8–12× faster than the nearest non-ultrapool pool on the smallest tasks. Sharded dispatch is what makes that possible.
- **Peak worker counts stay bounded.** Look at the raw benchmark files — competitors routinely spawn hundreds of thousands of goroutines under load (`fasthttp`: ~500k, raw goroutines: ~3M on Mutex_Contention). *ultrapool* caps out at a few tens of thousands and still wins on throughput.
- **v2 is a strict upgrade over v1.** Same API surface (plus generics), faster in every listed row.


## Author

*ultrapool* is written and maintained by [Moritz Fain](https://github.com/maurice2k).


## License

*ultrapool* is available under the MIT [license](LICENSE).
