package ultrapool

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	wp_tunny "github.com/Jeffail/tunny"
	wp_gammazero "github.com/gammazero/workerpool"
	"github.com/maurice2k/ultrapool"
	wp_fasthttp "github.com/maurice2k/ultrapool/benchmark/fasthttp"
	wp_ants "github.com/panjf2000/ants/v2"

	wp_pond "github.com/alitto/pond"
)

var wg sync.WaitGroup

var aesKey = []byte("0123456789ABCDEF")
var oneKiloByte = []byte(strings.Repeat("a", 1024))
var eightKiloByte = []byte(strings.Repeat("a", 8192))

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
}
var workLoadHandler func()

//// specific worker pool handler functions

// ultrapool handler function
func taskHandler(task *net.TCPConn) {
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

//// benchmarks

func BenchmarkPlainGoRoutines(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				b.ReportAllocs()
				b.SetParallelism(parallelism)
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						wg.Add(1)
						c := new(net.TCPConn)
						go taskHandler(c)
					}
				})
			})
		}
	}

	wg.Wait()
}

func BenchmarkAntsWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

				wp, _ := wp_ants.NewPoolWithFunc(10000000, taskHandlerAnts, wp_ants.WithPreAlloc(false), wp_ants.WithExpiryDuration(time.Second*5))

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

				b.StopTimer()
			})
		}
	}

}

func BenchmarkTunnyWorkerpool(b *testing.B) {
	for _, info := range workLoads {

		runtime.GC()

		workLoadHandler = info.handler
		for _, parallelism := range parellelisms {
			b.Run(fmt.Sprintf("%s/%d", info.name, parallelism), func(b *testing.B) {

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

				wp := wp_pond.New(2000, 2000, wp_pond.Strategy(wp_pond.Eager()))

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
				wp.SetIdleWorkerLifetime(time.Second * 5)

				//shards := runtime.GOMAXPROCS(0)
				//wp.SetNumShards(shards)
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
