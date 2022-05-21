# ultrapool

*ultrapool* is a blazing fast worker pool for Golang that supports adaptive spawning of new workers while idle workers are being cleaned up after a while.


## Architecture
*ultrapool* is modeled after the worker pool found in [valyala/fasthttp](https://github.com/valyala/fasthttp/blob/master/workerpool.go) but adds some speedups and is not bound to `net.Conn`.

Each worker has it's own channel that are queued in a slice structure whenever a worker is idle. The slice is guarded by a mutex.

While so many channels seem to be a performance issue it turns out that using only a few channels (which are then used by more than one worker) results in much poorer performance.

The main difference to fasthttp's approach is that *ultrapool* uses sharding and a small CAS cache to minimize the locking effects. Locking is done on shard level using a spin lock.


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

Benchmarks have been run on a `Intel(R) Xeon(R) CPU E5-2620 v3 @ 2.40GHz` CPU with 12 cores (+ HT) using Golang 1.18.2.

All worker pools are trying to reduce the overhead of spawning a new worker by re-using spawned workers (time-memory tradeoff).

This is most efficient with work loads that can be processed pretty fast (in the range of a few microseconds). The higher the processing time the less efficient a worker pool gets.

I've benchmarked a few work load scenarios:


### Scenario #1: Just sleep for 1 microsecond

*ultrapool* has a much better performance than spawning go routines.

```
BenchmarkPlainGoRoutines/Sleep_1ms/1-24                  2970315               414.0 ns/op           115 B/op          3 allocs/op
BenchmarkPlainGoRoutines/Sleep_1ms/10-24                 2422207               489.8 ns/op           123 B/op          3 allocs/op
BenchmarkPlainGoRoutines/Sleep_1ms/50-24                 1729738               765.1 ns/op           112 B/op          3 allocs/op
BenchmarkPlainGoRoutines/Sleep_1ms/100-24                1000000              1074 ns/op             126 B/op          3 allocs/op

BenchmarkUltrapoolWorkerpool/Sleep_1ms/1-24              5735512               199.5 ns/op             8 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/Sleep_1ms/10-24             5473467               220.5 ns/op             8 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/Sleep_1ms/50-24             5488731               225.3 ns/op             9 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/Sleep_1ms/100-24            5404957               235.7 ns/op            11 B/op          1 allocs/op

BenchmarkAntsWorkerpool/Sleep_1ms/1-24                    840319              1264 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool/Sleep_1ms/10-24                   904767              1130 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool/Sleep_1ms/50-24                  1081887              1070 ns/op               9 B/op          1 allocs/op
BenchmarkAntsWorkerpool/Sleep_1ms/100-24                 1132861              1053 ns/op               9 B/op          1 allocs/op

BenchmarkEasypoolWorkerpool/Sleep_1ms/1-24               5612767               226.4 ns/op            10 B/op          1 allocs/op
BenchmarkEasypoolWorkerpool/Sleep_1ms/10-24              5639330               214.0 ns/op            17 B/op          1 allocs/op
BenchmarkEasypoolWorkerpool/Sleep_1ms/50-24              5140176               217.8 ns/op            27 B/op          1 allocs/op
BenchmarkEasypoolWorkerpool/Sleep_1ms/100-24             4913846               220.6 ns/op            31 B/op          1 allocs/op

BenchmarkFasthttpWorkerpool/Sleep_1ms/1-24                989583              1242 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/Sleep_1ms/10-24               983121              1262 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/Sleep_1ms/50-24               928732              1245 ns/op               9 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/Sleep_1ms/100-24              838867              1279 ns/op              11 B/op          1 allocs/op

BenchmarkGammazeroWorkerpool/Sleep_1ms/1-24               719996              1395 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/Sleep_1ms/10-24              829106              1450 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/Sleep_1ms/50-24              750172              1417 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/Sleep_1ms/100-24             717574              1435 ns/op              24 B/op          2 allocs/op
```

### Scenario #2: Calculate SHA256 over 1024 bytes

*ultrapool* is still ~20% better than spawning go routines.

```
BenchmarkPlainGoRoutines/SHA256_1kB/1-24                 2555998               450.3 ns/op            32 B/op          2 allocs/op
BenchmarkPlainGoRoutines/SHA256_1kB/10-24                2323909               464.5 ns/op            32 B/op          2 allocs/op
BenchmarkPlainGoRoutines/SHA256_1kB/50-24                1814096               559.6 ns/op            46 B/op          2 allocs/op
BenchmarkPlainGoRoutines/SHA256_1kB/100-24               1909450               596.9 ns/op            41 B/op          2 allocs/op

BenchmarkUltrapoolWorkerpool/SHA256_1kB/1-24             3005290               397.8 ns/op             8 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/SHA256_1kB/10-24            2642110               428.1 ns/op            13 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/SHA256_1kB/50-24            2521020               472.0 ns/op            31 B/op          1 allocs/op
BenchmarkUltrapoolWorkerpool/SHA256_1kB/100-24           2216846               467.9 ns/op            36 B/op          1 allocs/op

BenchmarkAntsWorkerpool/SHA256_1kB/1-24                   988203              1179 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool/SHA256_1kB/10-24                 1059818              1080 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool/SHA256_1kB/50-24                 1081160              1066 ns/op               8 B/op          1 allocs/op
BenchmarkAntsWorkerpool/SHA256_1kB/100-24                1096756              1048 ns/op               9 B/op          1 allocs/op

BenchmarkEasypoolWorkerpool/SHA256_1kB/1-24              1864959               583.3 ns/op            68 B/op          1 allocs/op
BenchmarkEasypoolWorkerpool/SHA256_1kB/10-24             1000000              1691 ns/op             260 B/op          3 allocs/op
BenchmarkEasypoolWorkerpool/SHA256_1kB/50-24             2247285              1427 ns/op             235 B/op          3 allocs/op
BenchmarkEasypoolWorkerpool/SHA256_1kB/100-24            1000000              1255 ns/op             224 B/op          4 allocs/op

BenchmarkFasthttpWorkerpool/SHA256_1kB/1-24              1000000              1182 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/SHA256_1kB/10-24              956448              1244 ns/op               8 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/SHA256_1kB/50-24              858194              1226 ns/op               9 B/op          1 allocs/op
BenchmarkFasthttpWorkerpool/SHA256_1kB/100-24             946658              1212 ns/op               9 B/op          1 allocs/op

BenchmarkGammazeroWorkerpool/SHA256_1kB/1-24              715081              1550 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/SHA256_1kB/10-24             745165              1488 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/SHA256_1kB/50-24             741728              1465 ns/op              24 B/op          2 allocs/op
BenchmarkGammazeroWorkerpool/SHA256_1kB/100-24            707242              1463 ns/op              24 B/op          2 allocs/op
```


### Scenario #3: Encrypt 1024 byte using AES-128-CBC

Numbers have halved, but *ultrapool* is -- again -- still ~10% better than spawning go routines.

```
BenchmarkPlainGoRoutines/AES_CBC_1kB/1-24                1000000              2184 ns/op            3295 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_1kB/10-24                834814              2591 ns/op            3306 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_1kB/50-24                733308              2506 ns/op            3307 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_1kB/100-24               657081              2448 ns/op            3299 B/op         12 allocs/op

BenchmarkUltrapoolWorkerpool/AES_CBC_1kB/1-24            1000000              1753 ns/op            3279 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_1kB/10-24            859489              2151 ns/op            3301 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_1kB/50-24            850668              7569 ns/op            3343 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_1kB/100-24           832702              2548 ns/op            3353 B/op         11 allocs/op

BenchmarkAntsWorkerpool/AES_CBC_1kB/1-24                 1000000              2486 ns/op            3273 B/op         11 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_1kB/10-24                 898910              2346 ns/op            3280 B/op         11 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_1kB/50-24                 801080              2373 ns/op            3297 B/op         11 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_1kB/100-24                745029              2676 ns/op            3316 B/op         11 allocs/op

BenchmarkEasypoolWorkerpool/AES_CBC_1kB/1-24             1000000              2147 ns/op            3402 B/op         12 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_1kB/10-24            1000000              2219 ns/op            3437 B/op         13 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_1kB/50-24            1000000              2584 ns/op            3524 B/op         14 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_1kB/100-24           1000000              2836 ns/op            3537 B/op         14 allocs/op

BenchmarkFasthttpWorkerpool/AES_CBC_1kB/1-24             1000000              2079 ns/op            3281 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_1kB/10-24             851986              1738 ns/op            3279 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_1kB/50-24             773848              1904 ns/op            3289 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_1kB/100-24            643359              2105 ns/op            3295 B/op         11 allocs/op

BenchmarkGammazeroWorkerpool/AES_CBC_1kB/1-24             669787              2339 ns/op            3288 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_1kB/10-24            701192              2103 ns/op            3288 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_1kB/50-24            681267              2169 ns/op            3288 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_1kB/100-24           698540              2248 ns/op            3288 B/op         12 allocs/op
```


### Scenario #4: Encrypt 8192 bytes using AES-128-CBC

Overall, worker pools are getting less useful; *ultrapool* is still ~4% better than spawning go routines.

```
BenchmarkPlainGoRoutines/AES_CBC_8kB/1-24                1000000              7656 ns/op           20938 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_8kB/10-24                453580             11320 ns/op           20984 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_8kB/50-24                199285             12905 ns/op           21491 B/op         12 allocs/op
BenchmarkPlainGoRoutines/AES_CBC_8kB/100-24               113212              9477 ns/op           20273 B/op         12 allocs/op

BenchmarkUltrapoolWorkerpool/AES_CBC_8kB/1-24             497756              6773 ns/op           20950 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_8kB/10-24            486513              7189 ns/op           20971 B/op         11 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_8kB/50-24            444885              9082 ns/op           21037 B/op         12 allocs/op
BenchmarkUltrapoolWorkerpool/AES_CBC_8kB/100-24           414063             10335 ns/op           21054 B/op         12 allocs/op

BenchmarkAntsWorkerpool/AES_CBC_8kB/1-24                   99210             11919 ns/op           20951 B/op         11 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_8kB/10-24                  99079             11229 ns/op           20981 B/op         11 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_8kB/50-24                  93127             11519 ns/op           21048 B/op         12 allocs/op
BenchmarkAntsWorkerpool/AES_CBC_8kB/100-24                 78806             12913 ns/op           21079 B/op         13 allocs/op

BenchmarkEasypoolWorkerpool/AES_CBC_8kB/1-24              252812              7480 ns/op           21043 B/op         12 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_8kB/10-24             434630             11067 ns/op           21115 B/op         13 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_8kB/50-24             443392             10109 ns/op           21106 B/op         13 allocs/op
BenchmarkEasypoolWorkerpool/AES_CBC_8kB/100-24            389287             10792 ns/op           21213 B/op         13 allocs/op

BenchmarkFasthttpWorkerpool/AES_CBC_8kB/1-24              414046              8100 ns/op           20966 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_8kB/10-24             378027              8116 ns/op           20983 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_8kB/50-24             377748              8923 ns/op           20989 B/op         11 allocs/op
BenchmarkFasthttpWorkerpool/AES_CBC_8kB/100-24            356851             10058 ns/op           20997 B/op         11 allocs/op

BenchmarkGammazeroWorkerpool/AES_CBC_8kB/1-24             497506              7220 ns/op           20952 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_8kB/10-24            474954              6491 ns/op           20952 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_8kB/50-24            458689              6995 ns/op           20952 B/op         12 allocs/op
BenchmarkGammazeroWorkerpool/AES_CBC_8kB/100-24           422271              7307 ns/op           20953 B/op         12 allocs/op
```

## License

*ultrapool* is available under the MIT [license](LICENSE).
