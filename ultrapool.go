// Package ultrapool implements a blazing fast worker pool with adaptive
// spawning of new workers and per-shard buffered task queues.
//
// Workers compete to dequeue from their shard's task queue. New workers
// spawn on backpressure (visible queue backlog), and retire after an idle
// timeout once they're above the per-shard floor.
//
// Copyright 2019-2026 Moritz Fain
// Moritz Fain <moritz@fain.io>

package ultrapool

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

var ErrPoolOverload = errors.New("worker pool overloaded")
var ErrPoolStopped = errors.New("worker pool stopped")

type TaskHandlerFunc[T any] func(task T)

type WorkerPool[T any] struct {
	handlerFunc        TaskHandlerFunc[T]
	idleWorkerLifetime time.Duration
	numShards          int
	maxWorkers         int
	queueSize          int
	shardMinWorkers    int
	shardMaxWorkers    int
	shards             []*poolShard[T]
	notify             chan struct{}
	stopChan           chan struct{}
	doneChan           chan struct{}
	doneOnce           sync.Once
	mutex              sync.Mutex
	started            bool
	stopped            int32

	spawnedWorkers uint64
	_              [56]byte

	waiters uint64
}

type poolShard[T any] struct {
	wp        *WorkerPool[T]
	tqLock    sync.RWMutex
	taskQueue chan T
	workers   int64
}

const defaultIdleWorkerLifetime = time.Second
const maxShards = 128
const defaultQueueSize = 1024
const defaultShardMinWorkers = 2
const defaultShardMaxWorkers = 2048
const defaultNumShardsMin = 2
const defaultNumShardsMax = 48

// defaultNumShards returns GOMAXPROCS/2, clamped to [defaultNumShardsMin, defaultNumShardsMax].
func defaultNumShards() int {
	n := runtime.GOMAXPROCS(0) / 2
	if n < defaultNumShardsMin {
		n = defaultNumShardsMin
	}
	if n > defaultNumShardsMax {
		n = defaultNumShardsMax
	}
	if (n % 2) != 0 {
		n++
	}

	return n
}

// Creates a new WorkerPool with the given task handling function
func NewWorkerPool[T any](handlerFunc TaskHandlerFunc[T]) *WorkerPool[T] {
	wp := &WorkerPool[T]{
		handlerFunc:        handlerFunc,
		idleWorkerLifetime: defaultIdleWorkerLifetime,
		numShards:          defaultNumShards(),
		maxWorkers:         0,
		queueSize:          defaultQueueSize,
		shardMinWorkers:    defaultShardMinWorkers,
		shardMaxWorkers:    defaultShardMaxWorkers,
	}

	return wp
}

// Sets the maximum number of workers that may exist concurrently.
func (wp *WorkerPool[T]) SetMaxWorkers(n int) {
	if n < 0 {
		n = 0
	}
	wp.maxWorkers = n
}

// Sets the per-shard task queue capacity. Values below 16 are clamped to 16.
func (wp *WorkerPool[T]) SetQueueSize(size int) {
	if size < 16 {
		size = 16
	}
	wp.queueSize = size
}

// Sets the minimum number of workers per shard that are kept alive when idle.
// Also used as the initial worker count per shard at Start().
func (wp *WorkerPool[T]) SetShardMinWorkers(n int) {
	if n < 1 {
		n = 1
	}
	wp.shardMinWorkers = n
}

// Sets the maximum number of workers that may be spawned per shard.
// Acts as a per-shard backpressure cap independent of (and additional to)
// the global SetMaxWorkers cap. Values <= 0 reset to defaultShardMaxWorkers.
func (wp *WorkerPool[T]) SetShardMaxWorkers(n int) {
	if n <= 0 {
		n = defaultShardMaxWorkers
	}
	wp.shardMaxWorkers = n
}

// Sets number of shards. Values <= 0 reset to the runtime-derived default
// (GOMAXPROCS/4, clamped to [defaultNumShardsMin, defaultNumShardsMax]).
func (wp *WorkerPool[T]) SetNumShards(numShards int) {
	if numShards <= 0 {
		numShards = defaultNumShards()
	}
	if numShards > maxShards {
		numShards = maxShards
	}
	wp.numShards = numShards
}

// Sets the idle worker lifetime
func (wp *WorkerPool[T]) SetIdleWorkerLifetime(d time.Duration) {
	wp.idleWorkerLifetime = d
}

// Returns the number of currently spawned workers
func (wp *WorkerPool[T]) GetSpawnedWorkers() int {
	return int(atomic.LoadUint64(&wp.spawnedWorkers))
}

// Returns the number of shards
func (wp *WorkerPool[T]) GetNumShards() int {
	return wp.numShards
}

// Starts the worker pool
func (wp *WorkerPool[T]) Start() {
	wp.mutex.Lock()
	defer wp.mutex.Unlock()

	if wp.started {
		return
	}

	if wp.numShards <= 0 {
		wp.numShards = defaultNumShards()
	}

	wp.notify = make(chan struct{}, 1)
	wp.stopChan = make(chan struct{})
	wp.doneChan = make(chan struct{})
	wp.doneOnce = sync.Once{}

	for i := 0; i < wp.numShards; i++ {
		shard := &poolShard[T]{
			wp:        wp,
			taskQueue: make(chan T, wp.queueSize),
		}
		wp.shards = append(wp.shards, shard)

		// Start initial workers per shard
		for j := 0; j < wp.shardMinWorkers; j++ {
			shard.spawnWorker()
		}
	}

	wp.started = true
}

// Stops the worker pool
func (wp *WorkerPool[T]) Stop() {
	wp.mutex.Lock()
	defer wp.mutex.Unlock()

	if !wp.started || atomic.LoadInt32(&wp.stopped) != 0 {
		return
	}

	atomic.StoreInt32(&wp.stopped, 1)
	close(wp.stopChan)

	// Close each shard's taskQueue under tqLock. Lock waits for any in-flight
	// dispatcher's RLock to release, so no send can race the close. Late
	// dispatchers acquire RLock after this point and see wp.stopped == 1, so
	// they bail with ErrPoolStopped before touching the channel. Workers
	// drain buffered tasks and then see !ok on their next receive and exit.
	for _, shard := range wp.shards {
		shard.tqLock.Lock()
		close(shard.taskQueue)
		shard.tqLock.Unlock()
	}
}

// Stops the worker pool and blocks until all workers have exited.
func (wp *WorkerPool[T]) StopAndWait() {
	wp.Stop()
	<-wp.doneChan
}

// Stops the worker pool and waits up to timeout for all workers to exit.
// Returns true if all workers exited, false on timeout.
func (wp *WorkerPool[T]) StopWithTimeout(timeout time.Duration) bool {
	wp.Stop()
	select {
	case <-wp.doneChan:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Adds a new task
func (wp *WorkerPool[T]) AddTask(task T) error {
	if !wp.started {
		return errors.New("worker pool must be started first")
	}
	if atomic.LoadInt32(&wp.stopped) != 0 {
		return ErrPoolStopped
	}

	shard := wp.shards[randInt()%wp.numShards]
	return shard.dispatch(task)
}

// Adds a new task and blocks until submitted
func (wp *WorkerPool[T]) AddTaskWithBlocking(task T) error {
	err := wp.AddTask(task)
	if err == nil || err != ErrPoolOverload {
		return err
	}

	atomic.AddUint64(&wp.waiters, 1)
	for {
		err = wp.AddTask(task)
		if err == nil {
			n := atomic.AddUint64(&wp.waiters, ^uint64(0))
			if n > 0 {
				select {
				case wp.notify <- struct{}{}:
				default:
				}
			}
			return nil
		}
		if err != ErrPoolOverload {
			atomic.AddUint64(&wp.waiters, ^uint64(0))
			return err
		}

		select {
		case <-wp.notify:
		case <-wp.stopChan:
			atomic.AddUint64(&wp.waiters, ^uint64(0))
			return errors.New("worker pool stopped")
		}
	}
}

// dispatch enqueues a task and spawns a worker on visible backlog.
// The RLock fences the entire critical section (both send attempts and the
// spawn calls) against Stop's close of taskQueue. Late dispatchers re-check
// wp.stopped under the lock to close the TOCTOU window between AddTask's
// fast-path check and the actual send. A non-zero len() after a successful
// send means no idle worker grabbed the task directly, so it would have to
// wait — spawn one (capped).
func (shard *poolShard[T]) dispatch(task T) error {
	if len(shard.taskQueue) > 0 {
		//shard.trySpawnWorker()
	}

	shard.tqLock.RLock()

	if atomic.LoadInt32(&shard.wp.stopped) != 0 {
		shard.tqLock.RUnlock()
		return ErrPoolStopped
	}

	select {
	case shard.taskQueue <- task:
		if len(shard.taskQueue) > 0 {
			shard.trySpawnWorker()
		}

		shard.tqLock.RUnlock()
		return nil
	default:
	}

	// buffer full — spawn and retry once
	shard.trySpawnWorker()

	// retry a non-blocking enqueue; a worker may have drained the buffer after trySpawnWorker.
	select {
	case shard.taskQueue <- task:
		shard.tqLock.RUnlock()
		return nil
	default:
		shard.tqLock.RUnlock()
		return ErrPoolOverload
	}
}

// trySpawnWorker attempts to spawn a new worker for this shard, respecting both
// the per-shard cap (shardMaxWorkers) and the global cap (wp.maxWorkers).
// Both bounds are enforced atomically via CAS to prevent TOCTOU over-spawn
// when many dispatchers race the spawn decision.
func (shard *poolShard[T]) trySpawnWorker() bool {
	wp := shard.wp
	shardMax := int64(wp.shardMaxWorkers)
	// Reserve a per-shard slot atomically.
	for {
		cur := atomic.LoadInt64(&shard.workers)
		if cur >= shardMax {
			return false
		}
		if atomic.CompareAndSwapInt64(&shard.workers, cur, cur+1) {
			break
		}
	}

	// Reserve a global slot atomically (or unconditional add when no cap).
	if wp.maxWorkers > 0 {
		for {
			cur := atomic.LoadUint64(&wp.spawnedWorkers)
			if cur >= uint64(wp.maxWorkers) {
				// Roll back the per-shard reservation.
				atomic.AddInt64(&shard.workers, -1)
				return false
			}
			if atomic.CompareAndSwapUint64(&wp.spawnedWorkers, cur, cur+1) {
				break
			}
		}
	} else {
		atomic.AddUint64(&wp.spawnedWorkers, 1)
	}

	go shard.workerLoop()
	return true
}

// spawnWorker is used by Start() for initial worker creation; it bypasses
// the per-shard cap check (Start owns the bookkeeping itself).
func (shard *poolShard[T]) spawnWorker() {
	atomic.AddUint64(&shard.wp.spawnedWorkers, 1)
	atomic.AddInt64(&shard.workers, 1)
	go shard.workerLoop()
}

// workerLoop is the main worker goroutine. It reads from its shard's
// taskQueue. Workers above the per-shard floor exit after idleWorkerLifetime
// without receiving a task. On Stop, taskQueue is closed: buffered values
// drain first, then receives return !ok and the worker exits.
func (shard *poolShard[T]) workerLoop() {
	wp := shard.wp
	idleTimeout := wp.idleWorkerLifetime
	var idleTimer *time.Timer

	for {
		// Run queued work first; without any timer overhead. A closed channel
		// drains buffered values before returning !ok, so this naturally
		// handles "drain remaining tasks before exiting" on Stop.
		for {
			select {
			case task, ok := <-shard.taskQueue:
				if !ok {
					goto exit
				}
				wp.handlerFunc(task)
			default:
				goto idle
			}
		}

	idle:
		wp.notifyWaiter()

		// Floor workers wait indefinitely to keep the shard warm. Plain
		// chanrecv (the compiler skips selectgo for a single-case receive).
		if atomic.LoadInt64(&shard.workers) <= int64(wp.shardMinWorkers) {
			task, ok := <-shard.taskQueue
			if !ok {
				goto exit
			}
			wp.handlerFunc(task)
			continue
		}

		// Workers above the floor may retire after an idle timeout.
		if idleTimer == nil {
			idleTimer = time.NewTimer(idleTimeout)
		} else {
			idleTimer.Reset(idleTimeout)
		}

		select {
		case task, ok := <-shard.taskQueue:
			if !idleTimer.Stop() {
				// drain stale value (not required for Go 1.23+)
				select {
				case <-idleTimer.C:
				default:
				}
			}
			if !ok {
				goto exit
			}
			wp.handlerFunc(task)
		case <-idleTimer.C:
			for {
				workers := atomic.LoadInt64(&shard.workers)
				if workers <= int64(wp.shardMinWorkers) {
					break
				}
				// Only exit if the decrement keeps the shard at or above its floor.
				if atomic.CompareAndSwapInt64(&shard.workers, workers, workers-1) {
					goto exit2
				}
			}
		}
	}

exit:
	atomic.AddInt64(&shard.workers, -1)
exit2:
	atomic.AddUint64(&wp.spawnedWorkers, ^uint64(0))
	wp.notifyWaiter()
	if atomic.LoadInt32(&wp.stopped) != 0 && atomic.LoadUint64(&wp.spawnedWorkers) == 0 {
		wp.doneOnce.Do(func() { close(wp.doneChan) })
	}
}

func (wp *WorkerPool[T]) notifyWaiter() {
	if atomic.LoadUint64(&wp.waiters) == 0 {
		return
	}
	select {
	case wp.notify <- struct{}{}:
	default:
	}
}

// SplitMix64 style random pseudo number generator
type splitMix64 struct {
	state uint64
}

func (sm64 *splitMix64) Init(seed int64) {
	sm64.state = uint64(seed)
}

func (sm64 *splitMix64) Uint64() uint64 {
	sm64.state = sm64.state + uint64(0x9E3779B97F4A7C15)
	z := sm64.state
	z = (z ^ (z >> 30)) * uint64(0xBF58476D1CE4E5B9)
	z = (z ^ (z >> 27)) * uint64(0x94D049BB133111EB)
	return z ^ (z >> 31)
}

func (sm64 *splitMix64) Int63() int64 {
	return int64(sm64.Uint64() & (1<<63 - 1))
}

var splitMix64Pool = sync.Pool{
	New: func() any {
		sm64 := &splitMix64{}
		sm64.Init(time.Now().UnixNano())
		return sm64
	},
}

func randInt() (r int) {
	sm64 := splitMix64Pool.Get().(*splitMix64)
	r = int(sm64.Int63())
	splitMix64Pool.Put(sm64)
	return
}
