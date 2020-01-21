# ultrapool

*ultrapool* is a blazing fast worker pool for Golang that supports adaptive spawning of new workers while idle workers are being cleaned up after a while.
 

## Architecture
*ultrapool* is modeled after the worker pool found in [valyala/fasthttp](https://github.com/valyala/fasthttp/blob/master/workerpool.go) but adds some speedups and is not bound to `net.Conn`.

Each worker has it's own channel that are queued in a slice structure whenever a worker is idle. The slice is guarded by a mutex.

While so many channels seem to be a performance issue it turns out that using only a few channels (which are then used by more than one worker) results in much poorer performance.

The main difference to fasthttp's approach is that *ultrapool* uses sharding to minimize the locking effects and uses a spin lock for locking the shards.


## Example

```golang
wp := ultrapool.NewWorkerPool(func(task ultrapool.Task) {
    serveConn(task.(net.Conn))
})

// start the worker pool
wp.Start()

// ...

// add some task
wp.AddTask(conn)   // handle connection


// gracefully stop the pool
// Stop() does not wait but also does not terminate any running tasks
wp.Stop()

```


## Benchmarks

Benchmarks have been run on a `Intel(R) Xeon(R) CPU E5-2620 v3 @ 2.40GHz` CPU with 6 cores (+ HT) using Golang 1.13.3.

All worker pools are trying to reduce the overhead of spawning a new worker by re-using spawned workers (time-memory tradeoff).

This is most efficient with work loads that can be processed pretty fast (in the range of a few microseconds). The higher the processing time the less efficient a worker pool gets.

I've benchmarked a few work load scenarios: 
  
  
### Scenario #1: Just sleep for 1 microsecond

*ultrapool* has a much better performance than spawning go routines.

```
$ go test -bench=. benchmark/workerpool_test.go
goos: linux
goarch: amd64
BenchmarkNaiveGoRoutine-12               3491036               288 ns/op              95 B/op          2 allocs/op
BenchmarkUltrapoolWorkerpool-12          5988212               203 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool-12               2396034               502 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool-12           1944531               645 ns/op               8 B/op          1 allocs/op
BenchmarkGammazeroWorkerpool-12           924748              1311 ns/op              24 B/op          2 allocs/op
PASS
ok      command-line-arguments  8.074s
```

### Scenario #2: Calculate SHA256 over 1024 bytes

*ultrapool* is still ~9% better than spawning go routines.

```
$ go test -bench=. benchmark/workerpool_test.go
goos: linux
goarch: amd64
BenchmarkNaiveGoRoutine-12               1490734               754 ns/op              24 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool-12          1631008               734 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool-12               1585267               753 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool-12           1344253               811 ns/op              23 B/op          1 allocs/op
BenchmarkGammazeroWorkerpool-12           812283              1490 ns/op              24 B/op          2 allocs/op
PASS
ok      command-line-arguments  10.236s
```


### Scenario #3: Encrypt 1024 byte using AES-128-CBC

Numbers have halved, but *ultrapool* is -- again -- still ~9% better than spawning go routines.

```
$ go test -bench=. benchmark/workerpool_test.go
goos: linux
goarch: amd64
BenchmarkNaiveGoRoutine-12                601015              1760 ns/op            3037 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool-12           651818              2114 ns/op            3021 B/op         11 allocs/op
BenchmarkAntsWorkerpool-12                524216              2345 ns/op            3019 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool-12            457712              2954 ns/op            3029 B/op         11 allocs/op
BenchmarkGammazeroWorkerpool-12           467893              2716 ns/op            3032 B/op         12 allocs/op
PASS
ok      command-line-arguments  7.871s
```


### Scenario #4: Encrypt 8192 bytes using AES-128-CBC

Overall, worker pools are getting less useful; *ultrapool* is still ~2% better than spawning go routines.

```
$ go test -bench=. benchmark/workerpool_test.go
goos: linux
goarch: amd64
BenchmarkNaiveGoRoutine-12                136448              9147 ns/op           20409 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool-12           139228              9165 ns/op           20319 B/op         11 allocs/op
BenchmarkAntsWorkerpool-12                103660             10545 ns/op           20327 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool-12             81055             12352 ns/op           20403 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool-12           101978              9964 ns/op           20312 B/op         12 allocs/op
PASS
ok      command-line-arguments  9.744s
```


## License

*ultrapool* is available under the MIT [license](LICENSE).
