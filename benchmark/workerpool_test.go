package ultrapool

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"hash/crc32"
	"io"
	"math/rand/v2"
	"net"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	wp_tunny "github.com/Jeffail/tunny"
	wp_pond "github.com/alitto/pond/v2"
	wp_gammazero "github.com/gammazero/workerpool"
	wp_fasthttp "github.com/maurice2k/ultrapool/benchmark/fasthttp"
	wp_ultrapool_v1 "github.com/maurice2k/ultrapool/benchmark/ultrapool-v1"
	"github.com/maurice2k/ultrapool/v2"
	wp_ants "github.com/panjf2000/ants/v2"
)

var wg sync.WaitGroup

var aesKey = []byte("0123456789ABCDEF")
var oneKiloByte = []byte(strings.Repeat("a", 1024))
var eightKiloByte = []byte(strings.Repeat("a", 8192))
var sixtyFourBytes = []byte(strings.Repeat("x", 64))
var fourKiloBytes = make([]byte, 4096)
var mutexCounter atomic.Int64
var sharedMutex sync.Mutex

type workLoadInfo struct {
	name    string
	title   string
	handler func()
}

var parellelisms = []int{1, 10, 50, 100}
var workLoads = []workLoadInfo{
	{
		"Sleep_1ms",
		"Sleep for 1 microsecond",
		func() {
			time.Sleep(time.Microsecond)
		},
	},
	{
		"Sleep_50ms",
		"Sleep for 50 milliseconds (pins workers — exposes spawn behavior)",
		func() {
			time.Sleep(50 * time.Millisecond)
		},
	},
	{
		"SHA256_1kB",
		"SHA256 hash over 1kB",
		func() {
			sha256.Sum256(oneKiloByte)
		},
	},
	{
		"AES_CBC_1kB",
		"Encrypt 1kB with AES-CBC",
		func() {
			encryptCBC(oneKiloByte, aesKey)
		},
	},
	{
		"AES_CBC_8kB",
		"Encrypt 8kB with AES-CBC",
		func() {
			encryptCBC(eightKiloByte, aesKey)
		},
	},
	{
		"CRC32_64B",
		"CRC32 over 64 bytes (ultra-short CPU)",
		func() {
			crc32.ChecksumIEEE(sixtyFourBytes)
		},
	},
	{
		"MemScan_4kB",
		"Linear sum scan of 4kB buffer (memory-bound)",
		func() {
			var sum byte
			for _, b := range fourKiloBytes {
				sum += b
			}
			_ = sum
		},
	},
	{
		"Mixed_Bimodal",
		"80% fast (~100ns CRC32) + 20% slow (~5us AES-CBC)",
		func() {
			if rand.Uint32()%5 == 0 {
				encryptCBC(oneKiloByte, aesKey)
			} else {
				crc32.ChecksumIEEE(sixtyFourBytes)
			}
		},
	},
	{
		"Mutex_Contention",
		"Acquire shared mutex, increment counter",
		func() {
			sharedMutex.Lock()
			mutexCounter.Add(1)
			sharedMutex.Unlock()
		},
	},
}
var workLoadHandler func()

//// specific worker pool handler functions

// ultrapool handler function
func taskHandler(task *net.TCPConn) {
	workLoadHandler()
	wg.Done()
}

// ultrapool v1 handler function
func taskHandlerV1(task wp_ultrapool_v1.Task) {
	workLoadHandler()
	wg.Done()
}

// ants pool handler function
func taskHandlerAnts(task interface{}) {
	workLoadHandler()
	wg.Done()
}

// tunny pool handler function
func taskHandlerTunny(task interface{}) interface{} {
	workLoadHandler()
	wg.Done()
	return nil
}

// fasthttp handler function
func taskHandlerFasthttp(conn net.Conn) error {
	workLoadHandler()
	wg.Done()
	return nil
}

// Samples runtime goroutines and pool worker count during a benchmark run.
func startRuntimeSampler(getWorkers func() int) func() (int, int) {
	var peakGoroutines atomic.Int64
	var peakWorkers atomic.Int64
	done := make(chan struct{})

	updateMax := func(dst *atomic.Int64, v int64) {
		for {
			cur := dst.Load()
			if v <= cur || dst.CompareAndSwap(cur, v) {
				return
			}
		}
	}

	sample := func() {
		updateMax(&peakGoroutines, int64(runtime.NumGoroutine()))
		if getWorkers != nil {
			updateMax(&peakWorkers, int64(getWorkers()))
		}
	}

	sample()
	go func() {
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				sample()
			}
		}
	}()

	return func() (int, int) {
		close(done)
		sample()
		return int(peakGoroutines.Load()), int(peakWorkers.Load())
	}
}

//// benchmarks

func BenchmarkPlainGoRoutines(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				var inFlight atomic.Int64
				stopSampler := startRuntimeSampler(func() int { return int(inFlight.Load()) })

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						go func(c *net.TCPConn) {
							inFlight.Add(1)
							taskHandler(c)
							inFlight.Add(-1)
						}(c)
					}
				})
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")
			})
		}
	}
}

func BenchmarkAntsWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp, _ := wp_ants.NewPoolWithFunc(10000000, taskHandlerAnts, wp_ants.WithPreAlloc(false), wp_ants.WithExpiryDuration(time.Second*15))

				stopSampler := startRuntimeSampler(func() int { return wp.Running() })

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						wp.Invoke(c)
					}
				})

				wp.Release()
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")

				b.StopTimer()
			})
		}
	}

}

func BenchmarkTunnyWorkerpool(b *testing.B) {
	for _, workLoad := range workLoads {

		runtime.GC()

		workLoadHandler = workLoad.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", workLoad.name, parallelism), func(b *testing.B) {

				wp := wp_tunny.NewFunc(runtime.GOMAXPROCS(0), taskHandlerTunny)

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						wp.Process(c)
					}
				})

				wp.Close()
				wg.Wait()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")

				b.StopTimer()
			})
		}
	}

}

func BenchmarkPondWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp := wp_pond.NewPool(10000000)

				stopSampler := startRuntimeSampler(func() int { return int(wp.RunningWorkers()) })

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						wp.Submit(func() {
							taskHandlerFasthttp(c)
						})
					}
				})

				wp.StopAndWait()
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")

				b.StopTimer()
			})
		}
	}

}

func BenchmarkUltrapoolWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp := ultrapool.NewWorkerPool(taskHandler)
				wp.SetIdleWorkerLifetime(time.Second * 15)

				wp.Start()

				stopSampler := startRuntimeSampler(func() int { return wp.GetSpawnedWorkers() })

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						if err := wp.AddTaskWithBlocking(c); err != nil {
							wg.Done()
						}
					}
				})

				wp.Stop()
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")

				b.StopTimer()

				runtime.GC()
				time.Sleep(100 * time.Millisecond)

			})
		}

	}
}

func BenchmarkUltrapoolV1Workerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp := wp_ultrapool_v1.NewWorkerPool(taskHandlerV1)
				wp.SetIdleWorkerLifetime(time.Second * 15)
				numShards := runtime.GOMAXPROCS(0) / 4
				if numShards < 2 {
					numShards = 2
				}
				if numShards > 12 {
					numShards = 12
				}
				wp.SetNumShards(numShards)

				wp.Start()

				stopSampler := startRuntimeSampler(func() int { return wp.GetSpawnedWorkers() })

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						_ = wp.AddTask(c)
					}
				})

				wp.Stop()
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")

				b.StopTimer()

				runtime.GC()
				time.Sleep(100 * time.Millisecond)

			})
		}

	}
}

// BenchmarkUltrapoolSlowBurst tests scaling under a small burst of slow tasks.
// 50 tasks × 2s sleep:
//   - Ideal (one goroutine each):       ~2s wall-clock
//   - 24 workers, no spawning:          ~4s (two rounds)
//   - Aggressive spawning to ~50:       ~2s
//
// Two variants:
//   - "multishard": tasks distributed across 12 shards (~4 tasks/shard)
//     — buffer rarely fills, so spawn-on-full triggers don't fire.
//   - "singleshard": all tasks go to shard 0 — buffer fills if queueSize is small.
//
// Run with:
//
//	go test -tags=ring -run='^$' -bench='BenchmarkUltrapoolSlowBurst' \
//	    -benchtime=3x -count=3 -timeout=300s .
func BenchmarkUltrapoolSlowBurst(b *testing.B) {
	const burstSize = 50
	const sleepDur = 2 * time.Second
	// queueSize stays at the library default (1024). The whole point of this
	// benchmark is to expose how the spawn-decision logic behaves when the
	// buffer is large enough to hide the load signal.

	workLoadHandler = func() { time.Sleep(sleepDur) }

	wp := ultrapool.NewWorkerPool(taskHandler)
	wp.SetIdleWorkerLifetime(10_000 * time.Millisecond)
	wp.SetMaxWorkers(10_000 * wp.GetNumShards())
	wp.SetNumShards(4)
	wp.Start()

	stopSampler := startRuntimeSampler(func() int { return wp.GetSpawnedWorkers() })

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(burstSize)
		for j := 0; j < burstSize; j++ {
			c := new(net.TCPConn)
			_ = wp.AddTaskWithBlocking(c)
		}
		wg.Wait()

		b.StopTimer()
		time.Sleep(300 * time.Millisecond) // age workers out
		b.StartTimer()
	}

	wp.Stop()
	_, peakWorkers := stopSampler()
	b.ReportMetric(float64(peakWorkers), "peak-workers")
	b.ReportMetric(float64(burstSize), "burst-size")
}

func BenchmarkUltrapoolV1SlowBurst(b *testing.B) {
	const burstSize = 50
	const sleepDur = 2 * time.Second

	workLoadHandler = func() { time.Sleep(sleepDur) }

	wp := wp_ultrapool_v1.NewWorkerPool(taskHandlerV1)
	wp.SetIdleWorkerLifetime(100 * time.Millisecond)
	wp.Start()

	stopSampler := startRuntimeSampler(func() int { return wp.GetSpawnedWorkers() })

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(burstSize)
		for j := 0; j < burstSize; j++ {
			c := new(net.TCPConn)
			_ = wp.AddTask(c)
		}
		wg.Wait()

		b.StopTimer()
		time.Sleep(300 * time.Millisecond) // age workers out
		b.StartTimer()
	}

	wp.Stop()
	_, peakWorkers := stopSampler()
	b.ReportMetric(float64(peakWorkers), "peak-workers")
	b.ReportMetric(float64(burstSize), "burst-size")
}

// Cross-library SlowBurst benchmarks for fasthttp, ants, and goroutines.
// Same shape as BenchmarkUltrapoolSlowBurst/multishard so results are
// directly comparable.

const slowBurstSize = 50
const slowBurstSleep = 2 * time.Second
const slowBurstIdle = 300 * time.Millisecond

func BenchmarkFasthttpSlowBurst(b *testing.B) {
	workLoadHandler = func() { time.Sleep(slowBurstSleep) }

	wp := &wp_fasthttp.WorkerPool{
		WorkerFunc:            taskHandlerFasthttp,
		MaxWorkersCount:       10_000_000,
		LogAllErrors:          false,
		Logger:                nil,
		MaxIdleWorkerDuration: 100 * time.Millisecond,
	}
	wp.Start()

	stopSampler := startRuntimeSampler(func() int { return wp.GetWorkersCount() })

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(slowBurstSize)
		for j := 0; j < slowBurstSize; j++ {
			c := new(net.TCPConn)
			wp.Serve(c)
		}
		wg.Wait()

		b.StopTimer()
		time.Sleep(slowBurstIdle)
		b.StartTimer()
	}

	wp.Stop()
	_, peakWorkers := stopSampler()
	b.ReportMetric(float64(peakWorkers), "peak-workers")
	b.ReportMetric(float64(slowBurstSize), "burst-size")
}

func BenchmarkAntsSlowBurst(b *testing.B) {
	workLoadHandler = func() { time.Sleep(slowBurstSleep) }

	wp, _ := wp_ants.NewPoolWithFunc(10_000_000, taskHandlerAnts,
		wp_ants.WithPreAlloc(false),
		wp_ants.WithExpiryDuration(100*time.Millisecond))

	stopSampler := startRuntimeSampler(func() int { return wp.Running() })

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(slowBurstSize)
		for j := 0; j < slowBurstSize; j++ {
			c := new(net.TCPConn)
			_ = wp.Invoke(c)
		}
		wg.Wait()

		b.StopTimer()
		time.Sleep(slowBurstIdle)
		b.StartTimer()
	}

	wp.Release()
	_, peakWorkers := stopSampler()
	b.ReportMetric(float64(peakWorkers), "peak-workers")
	b.ReportMetric(float64(slowBurstSize), "burst-size")
}

func BenchmarkPlainGoSlowBurst(b *testing.B) {
	workLoadHandler = func() { time.Sleep(slowBurstSleep) }

	var inFlight atomic.Int64
	stopSampler := startRuntimeSampler(func() int { return int(inFlight.Load()) })

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		wg.Add(slowBurstSize)
		for j := 0; j < slowBurstSize; j++ {
			c := new(net.TCPConn)
			go func(c *net.TCPConn) {
				inFlight.Add(1)
				taskHandler(c)
				inFlight.Add(-1)
			}(c)
		}
		wg.Wait()

		b.StopTimer()
		time.Sleep(slowBurstIdle)
		b.StartTimer()
	}

	_, peakWorkers := stopSampler()
	b.ReportMetric(float64(peakWorkers), "peak-workers")
	b.ReportMetric(float64(slowBurstSize), "burst-size")
}

// BenchmarkUltrapoolBurst exercises bursty, sub-saturation workloads.
// Each iteration submits `burstSize` tasks, waits for them to finish, then
// sleeps so workers can age out back toward shardMinWorkers before the next
// burst. This isolates the spawn-decision logic (cold-start latency) instead
// of measuring steady-state saturation.
//
// Run only this benchmark with:
//
//	go test -tags=ring -run='^$' -bench='BenchmarkUltrapoolBurst' \
//	    -benchtime=200x -count=5 -timeout=300s .
//
// (-benchtime=200x => exactly 200 bursts per measurement)
func BenchmarkUltrapoolBurst(b *testing.B) {
	burstSizes := []int{50, 500, 5000}
	idleSleep := 50 * time.Millisecond

	for _, info := range workLoads {
		workLoadHandler = info.handler

		for _, burstSize := range burstSizes {
			b.Run(fmt.Sprintf("%s/burst_%d", info.name, burstSize), func(b *testing.B) {
				wp := ultrapool.NewWorkerPool(taskHandler)
				wp.SetIdleWorkerLifetime(20 * time.Millisecond) // age out fast between bursts
				numShards := runtime.GOMAXPROCS(0) / 4
				if numShards < 2 {
					numShards = 2
				}
				if numShards > 12 {
					numShards = 12
				}
				wp.SetNumShards(numShards)
				wp.SetMaxWorkers(10_000 * numShards)
				wp.Start()

				stopSampler := startRuntimeSampler(func() int { return wp.GetSpawnedWorkers() })

				b.ResetTimer()
				b.ReportAllocs()

				for i := 0; i < b.N; i++ {
					var localWG sync.WaitGroup
					localWG.Add(burstSize)
					// Override taskHandler's wg.Done with our local one for this burst.
					// (taskHandler uses the package-level wg, so we need to also Add to it.)
					wg.Add(burstSize)
					for j := 0; j < burstSize; j++ {
						c := new(net.TCPConn)
						_ = wp.AddTaskWithBlocking(c)
					}
					wg.Wait() // wait for all tasks in this burst to complete
					_ = localWG
					b.StopTimer()
					time.Sleep(idleSleep) // let workers age out
					b.StartTimer()
				}

				wp.Stop()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N*burstSize)/b.Elapsed().Seconds(), "tasks/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")
				b.ReportMetric(float64(burstSize), "burst-size")
			})
		}
	}
}

/*
	func BenchmarkEasypoolWorkerpool(b *testing.B) {
		for _, info := range workLoads {

			runtime.GC()

			workLoadHandler = info.handler
			for _, parallelism := range parellelisms {
				b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

					wp := NewWorkerPool(taskHandler2)

					shards := runtime.GOMAXPROCS(0)
					shards = 1
					wp.SetNumShards(shards)
					wp.Start()

					b.ResetTimer()

					b.ReportAllocs()
					b.SetParallelism(parallelism)
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							wg.Add(1)
							c := new(net.TCPConn)
							wp.AddTask(c)
						}
					})

					wp.Stop()
					wg.Wait()

					b.StopTimer()

				})
			}

		}
	}
*/
func BenchmarkFasthttpWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp := &wp_fasthttp.WorkerPool{
					WorkerFunc:            taskHandlerFasthttp,
					MaxWorkersCount:       10000000,
					LogAllErrors:          false,
					Logger:                nil,
					MaxIdleWorkerDuration: time.Second * 15,
				}
				wp.Start()

				stopSampler := startRuntimeSampler(func() int { return wp.GetWorkersCount() })

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						wp.Serve(c)
					}
				})

				wp.Stop()
				wg.Wait()
				_, peakWorkers := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakWorkers), "peak-workers")

				b.StopTimer()
			})
		}
	}

}

func BenchmarkGammazeroWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp := wp_gammazero.New(10000000)

				stopSampler := startRuntimeSampler(nil)

				b.ResetTimer()

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						wp.Submit(func() {
							taskHandlerFasthttp(c)
						})
					}
				})

				wp.Stop()
				wg.Wait()
				peakGoroutines, _ := stopSampler()
				b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
				b.ReportMetric(float64(peakGoroutines), "peak-workers")

				b.StopTimer()

			})
		}
	}
}

// Encrypts given cipher text (prepended with the IV) with AES-128 or AES-256
// (depending on the length of the key)
func encryptCBC(plainText, key []byte) (cipherText []byte, err error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	plainText = pad(aes.BlockSize, plainText)

	cipherText = make([]byte, aes.BlockSize+len(plainText))
	iv := cipherText[:aes.BlockSize]
	_, err = io.ReadFull(cryptoRand.Reader, iv)
	if err != nil {
		return nil, err
	}

	mode := cipher.NewCBCEncrypter(block, iv)
	mode.CryptBlocks(cipherText[aes.BlockSize:], plainText)

	return cipherText, nil
}

// Adds PKCS#7 padding (variable block length <= 255 bytes)
func pad(blockSize int, buf []byte) []byte {
	padLen := blockSize - (len(buf) % blockSize)
	padding := bytes.Repeat([]byte{byte(padLen)}, padLen)
	return append(buf, padding...)
}

////////////////////
/*
type Task interface{}
type TaskHandlerFunc func(task Task)

const (
	StateNew uint64 = iota
	stateShutdownForbidden
	stateShutdownPossible
	StateRunning
	StateStopped
)

type WorkerPool struct {
	handlerFunc TaskHandlerFunc
	workers     []sync.Pool
	mutex       sync.Mutex
	numShards   int
	started     bool
	stopped     bool
}

type worker struct {
	wp       *WorkerPool
	state    uint64
	running  bool
	taskChan chan Task
}

func taskHandler2(task *net.TCPConn) {
	workLoadHandler()
	wg.Done()
}

func NewWorkerPool(handlerFunc TaskHandlerFunc) *WorkerPool {
	wp := &WorkerPool{
		handlerFunc: handlerFunc,
	}

	return wp
}

func (wp *WorkerPool) Start() {
	wp.mutex.Lock()
	defer wp.mutex.Unlock()

	if wp.started {
		return
	}

	for i := 0; i < wp.numShards; i++ {
		wp.workers = append(wp.workers, sync.Pool{})
	}

	wp.started = true
}

func (wp *WorkerPool) Stop() {
	wp.stopped = true
}

// Sets number of shards (default is GOMAXPROCS shards)
func (wp *WorkerPool) SetNumShards(numShards int) {
	if numShards <= 1 {
		numShards = 1
	}

	if numShards > 128 {
		numShards = 128
	}

	wp.numShards = numShards
}

func (wp *WorkerPool) getWorker() (w *worker) {
	shardIdx := randInt() % wp.numShards
	v := wp.workers[shardIdx].Get()
	if v == nil {
		w = &worker{
			wp:       wp,
			state:    StateNew,
			taskChan: make(chan Task, 0),
		}
	} else {
		w = v.(*worker)
	}

	if !atomic.CompareAndSwapUint64(&w.state, (stateShutdownPossible), (stateShutdownForbidden)) {
		// go routing is winding down; restart
		go w.run(shardIdx)
	}

	return
}

func (wp *WorkerPool) releaseWorker(w *worker, shardIdx int) {
	wp.workers[shardIdx].Put(w)
}

func (wp *WorkerPool) AddTask(task Task) {
	w := wp.getWorker()
	w.taskChan <- task
}

func (w *worker) run(shardIdx int) {
	defer func() {
		w.running = false
	}()

	timeout := time.NewTicker(10 * time.Second)
	for {
		select {
		case task := <-w.taskChan:
			//atomic.StoreUint64(&w.state, stateShutdownPossible)
			w.state = stateShutdownPossible
			w.wp.handlerFunc(task)
			w.wp.releaseWorker(w, shardIdx)
		case <-timeout.C:
			if atomic.CompareAndSwapUint64(&w.state, stateShutdownPossible, StateStopped) {
				return
			}
		}
	}

}

// SplitMix64 style random pseudo number generator
type splitMix64 struct {
	state uint64
}

// Initialize SplitMix64
func (sm64 *splitMix64) Init(seed int64) {
	sm64.state = uint64(seed)
}

// Uint64 returns the next SplitMix64 pseudo-random number as a uint64
func (sm64 *splitMix64) Uint64() uint64 {
	sm64.state = sm64.state + uint64(0x9E3779B97F4A7C15)
	z := sm64.state
	z = (z ^ (z >> 30)) * uint64(0xBF58476D1CE4E5B9)
	z = (z ^ (z >> 27)) * uint64(0x94D049BB133111EB)
	return z ^ (z >> 31)

}

// Int63 returns a non-negative pseudo-random 63-bit integer as an int64
func (sm64 *splitMix64) Int63() int64 {
	return int64(sm64.Uint64() & (1<<63 - 1))
}

var splitMix64Pool sync.Pool = sync.Pool{
	New: func() interface{} {
		sm64 := &splitMix64{}
		sm64.Init(time.Now().UnixNano())
		return sm64
	},
}
var sm64g = new(splitMix64)

func randInt() (r int) {
	//sm64 := splitMix64Pool.Get().(*splitMix64)
	r = int(sm64g.Int63())
	//splitMix64Pool.Put(sm64)
	return
}
*/
