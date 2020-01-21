// Package ultrapool implements a blazing fast worker pool with adaptive
// spawning of new workers and cleanup of idle workers
// It was modeled after valyala/fasthttp's worker pool which is one of the
// best worker pools I've seen in the Go world.

// Copyright 2019-2020 Moritz Fain
// Moritz Fain <moritz@fain.io>
//
// Source available at github.com/maurice2k/ultrapool,
// licensed under the MIT license (see LICENSE file).

package ultrapool

import (
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

type Task interface{}
type TaskHandlerFunc func(task Task)

type WorkerPool struct {
	handlerFunc        TaskHandlerFunc
	idleWorkerLifetime time.Duration
	numShards          int
	shards             []*poolShard
	acquireCounter     int
	spawnedWorkers     uint64
	mutex              spinLocker
	started            bool
	stopped            bool
	stopChan           chan bool
	workerCache        sync.Pool
	currentTime        time.Time
}

type workerInstance struct {
	lastUsed  time.Time
	isDeleted bool
	taskChan  chan Task
	shard     *poolShard
}

type poolShard struct {
	wp             *WorkerPool
	idleWorkerList []*workerInstance
	mutex          spinLocker
	stopped        bool
}

const defaultIdleWorkerLifetime = time.Second

// Creates a new workerInstance pool with the given task handling function
func NewWorkerPool(handlerFunc TaskHandlerFunc) *WorkerPool {
	wp := &WorkerPool{
		handlerFunc:        handlerFunc,
		idleWorkerLifetime: defaultIdleWorkerLifetime,
		numShards:          runtime.GOMAXPROCS(0),
		acquireCounter:     0,
		workerCache: sync.Pool{
			New: func() interface{} {
				return &workerInstance{
					taskChan: make(chan Task, 1),
				}
			},
		},
	}

	return wp
}

// Sets number of shards (default is 2 shards)
func (wp *WorkerPool) SetNumShards(numShards int) {
	if numShards <= 1 {
		numShards = 1
	}
	wp.numShards = numShards
}

// Sets the time after which idling workers are shut down (default is 15 seconds)
func (wp *WorkerPool) SetIdleWorkerLifetime(d time.Duration) {
	wp.idleWorkerLifetime = d
}

// Returns the number of currently spawned workers
func (wp *WorkerPool) GetSpawnedWorkers() int {
	return int(atomic.LoadUint64(&wp.spawnedWorkers))
}

// Starts the worker pool
func (wp *WorkerPool) Start() {
	wp.mutex.Lock()
	if !wp.started {
		wp.currentTime = time.Now()
		for i := 0; i < wp.numShards; i++ {
			shard := &poolShard{
				wp: wp,
			}
			wp.shards = append(wp.shards, shard)
		}

		wp.started = true
	}
	wp.mutex.Unlock()

	go wp.cleanup()
	go func() {
		time.Sleep(time.Second)
		wp.currentTime = time.Now()
	}()
}

// Stops the worker pool.
// All tasks that have been added will be processed before shutdown.
func (wp *WorkerPool) Stop() {
	wp.mutex.Lock()
	if !wp.started {
		wp.mutex.Unlock()
		return
	}

	if !wp.stopped {

		for i := 0; i < wp.numShards; i++ {
			shard := wp.shards[i]
			shard.mutex.Lock()
			shard.stopped = true
			for j := 0; j < len(shard.idleWorkerList); j++ {
				close(shard.idleWorkerList[j].taskChan)
			}
			shard.mutex.Unlock()
		}
	}
	wp.stopped = true
	wp.mutex.Unlock()
}

// Adds a new task
func (wp *WorkerPool) AddTask(task Task) error {
	if !wp.started {
		return errors.New("Worker pool must be started first!")
	}

	worker := wp.shards[wp.acquireCounter%wp.numShards].getWorker()
	if worker == nil {
		return errors.New("Worker pool has already been stopped!")
	}

	worker.taskChan <- task
	wp.acquireCounter++

	return nil
}

// Returns next free worker or spawns a new worker
func (shard *poolShard) getWorker() (worker *workerInstance) {
	iws := len(shard.idleWorkerList)
	if iws > 0 {
		shard.mutex.Lock()
		if shard.stopped {
			shard.mutex.Unlock()
			return nil
		}
		iws = len(shard.idleWorkerList)
		if iws > 0 {
			worker = shard.idleWorkerList[iws-1]
			shard.idleWorkerList[iws-1] = nil
			shard.idleWorkerList = shard.idleWorkerList[0 : iws-1]
			shard.mutex.Unlock()
			return worker
		}
		shard.mutex.Unlock()
	}

	worker = shard.wp.workerCache.Get().(*workerInstance)
	worker.shard = shard
	if worker.isDeleted {
		worker.taskChan = make(chan Task, 1)
		worker.isDeleted = false
	}

	go worker.run()

	return worker
}

// Main worker runner
func (worker *workerInstance) run() {
	atomic.AddUint64(&worker.shard.wp.spawnedWorkers, +1)

	for task := range worker.taskChan {
		worker.shard.wp.handlerFunc(task)
		worker.lastUsed = time.Now()
		worker.shard.mutex.Lock()
		if !worker.shard.stopped {
			worker.shard.idleWorkerList = append(worker.shard.idleWorkerList, worker)
		}
		worker.shard.mutex.Unlock()
	}

	atomic.AddUint64(&worker.shard.wp.spawnedWorkers, ^uint64(0))
	worker.shard.wp.workerCache.Put(worker)
}

// Worker cleanup
func (wp *WorkerPool) cleanup() {
	var toBeCleaned []*workerInstance
	for {
		time.Sleep(wp.idleWorkerLifetime)
		if wp.stopped {
			return
		}

		now := time.Now()

		for i := 0; i < wp.numShards; i++ {
			shard := wp.shards[i]

			shard.mutex.Lock()
			idleWorkerList := shard.idleWorkerList
			iws := len(idleWorkerList)

			j := 0
			for j = 0; j < iws; j++ {
				if now.Sub(idleWorkerList[j].lastUsed) < wp.idleWorkerLifetime {
					break
				}
			}

			if j == 0 {
				shard.mutex.Unlock()
				continue
			}

			toBeCleaned = append(toBeCleaned[:0], idleWorkerList[0:j]...)

			numMoved := copy(idleWorkerList, idleWorkerList[j:])
			for j = numMoved; j < iws; j++ {
				idleWorkerList[j] = nil
			}
			shard.idleWorkerList = idleWorkerList[:numMoved]
			shard.mutex.Unlock()

			for j = 0; j < len(toBeCleaned); j++ {
				if !toBeCleaned[j].shard.stopped {
					toBeCleaned[j].isDeleted = true
					close(toBeCleaned[j].taskChan)
					toBeCleaned[j] = nil
				}
			}
		}
	}
}

type spinLocker uint64

func (s *spinLocker) Lock() {
	for !atomic.CompareAndSwapUint64((*uint64)(s), 0, 1) {
		runtime.Gosched()
	}
}

func (s *spinLocker) Unlock() {
	atomic.StoreUint64((*uint64)(s), 0)
}
