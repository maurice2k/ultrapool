package ultrapool

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDefaultNumShards(t *testing.T) {
	expected := runtime.GOMAXPROCS(0) / 2
	if (expected % 2) != 0 {
		expected++
	}
	if expected < defaultNumShardsMin {
		expected = defaultNumShardsMin
	}
	if expected > defaultNumShardsMax {
		expected = defaultNumShardsMax
	}

	// NewWorkerPool applies the default at init.
	wp := NewWorkerPool(func(task int) {})
	if wp.numShards != expected {
		t.Errorf("NewWorkerPool numShards: got %d, want %d", wp.numShards, expected)
	}

	// SetNumShards(0) resets to the default.
	wp.SetNumShards(64)
	if wp.numShards != 64 {
		t.Fatalf("SetNumShards(64): got %d, want 64", wp.numShards)
	}
	wp.SetNumShards(0)
	if wp.numShards != expected {
		t.Errorf("SetNumShards(0): got %d, want %d", wp.numShards, expected)
	}

	// SetNumShards(-1) also resets to the default.
	wp.SetNumShards(64)
	wp.SetNumShards(-1)
	if wp.numShards != expected {
		t.Errorf("SetNumShards(-1): got %d, want %d", wp.numShards, expected)
	}

	// Explicit 1 is respected (single-shard pools are still valid).
	wp.SetNumShards(1)
	if wp.numShards != 1 {
		t.Errorf("SetNumShards(1): got %d, want 1", wp.numShards)
	}

	// maxShards cap still applies.
	wp.SetNumShards(maxShards + 100)
	if wp.numShards != maxShards {
		t.Errorf("SetNumShards above max: got %d, want %d", wp.numShards, maxShards)
	}

	// Default bounds: between 2 and 12.
	if expected < 2 || expected > 12 {
		t.Errorf("default bounds violated: expected=%d, want [2, 12]", expected)
	}
}

func TestShardWorkerDefaults(t *testing.T) {
	wp := NewWorkerPool(func(task int) {})
	if wp.shardMaxWorkers != defaultShardMaxWorkers {
		t.Errorf("default shardMaxWorkers: got %d, want %d", wp.shardMaxWorkers, defaultShardMaxWorkers)
	}
	if defaultShardMaxWorkers != 2048 {
		t.Errorf("defaultShardMaxWorkers constant: got %d, want 2048", defaultShardMaxWorkers)
	}
	if wp.shardMinWorkers != defaultShardMinWorkers {
		t.Errorf("default shardMinWorkers: got %d, want %d", wp.shardMinWorkers, defaultShardMinWorkers)
	}
}

func TestSetShardWorkerBounds(t *testing.T) {
	wp := NewWorkerPool(func(task int) {})

	wp.SetShardMaxWorkers(64)
	if wp.shardMaxWorkers != 64 {
		t.Errorf("SetShardMaxWorkers(64): got %d, want 64", wp.shardMaxWorkers)
	}

	wp.SetShardMaxWorkers(0)
	if wp.shardMaxWorkers != defaultShardMaxWorkers {
		t.Errorf("SetShardMaxWorkers(0) should restore default: got %d, want %d", wp.shardMaxWorkers, defaultShardMaxWorkers)
	}

	wp.SetShardMaxWorkers(-5)
	if wp.shardMaxWorkers != defaultShardMaxWorkers {
		t.Errorf("SetShardMaxWorkers(-5) should restore default: got %d, want %d", wp.shardMaxWorkers, defaultShardMaxWorkers)
	}

	wp.SetShardMinWorkers(8)
	if wp.shardMinWorkers != 8 {
		t.Errorf("SetShardMinWorkers(8): got %d, want 8", wp.shardMinWorkers)
	}

	wp.SetShardMinWorkers(0)
	if wp.shardMinWorkers != 1 {
		t.Errorf("SetShardMinWorkers(0) should clamp to 1: got %d", wp.shardMinWorkers)
	}

	wp.SetShardMinWorkers(-3)
	if wp.shardMinWorkers != 1 {
		t.Errorf("SetShardMinWorkers(-3) should clamp to 1: got %d", wp.shardMinWorkers)
	}
}

func TestShardWorkerBoundsApplied(t *testing.T) {
	const minWorkers = 3
	const maxWorkers = 7
	const shards = 2
	const tasksPerShard = 50

	var wg sync.WaitGroup
	wg.Add(tasksPerShard * shards)

	wp := NewWorkerPool(func(task int) {
		time.Sleep(50 * time.Millisecond)
		wg.Done()
	})
	wp.SetNumShards(shards)
	wp.SetShardMinWorkers(minWorkers)
	wp.SetShardMaxWorkers(maxWorkers)
	wp.Start()
	defer wp.Stop()

	// At Start, each shard should have spawned exactly shardMinWorkers.
	if got := wp.GetSpawnedWorkers(); got != minWorkers*shards {
		t.Errorf("after Start: spawned workers got %d, want %d", got, minWorkers*shards)
	}

	// Saturate beyond shardMaxWorkers per shard to confirm the cap holds.
	for s := 0; s < shards; s++ {
		for i := 0; i < tasksPerShard; i++ {
			if err := wp.AddTaskWithBlocking(i); err != nil {
				t.Fatalf("AddTaskWithBlocking: %v", err)
			}
		}
	}

	// Give the pool a moment to spawn up to the cap.
	time.Sleep(20 * time.Millisecond)
	if got := wp.GetSpawnedWorkers(); got > maxWorkers*shards {
		t.Errorf("spawned workers exceeded cap: got %d, want <= %d", got, maxWorkers*shards)
	}

	wg.Wait()
}

// engageBlockedPool starts a 1-shard pool with shardMax workers, blocks every
// handler on `release`, and waits until all shardMax workers are actually
// running their handler before returning. This avoids the race where a worker
// goroutine spawned via trySpawnWorker has not yet been scheduled — that
// late-starting worker would dequeue from a "full" buffer and silently free a
// slot, breaking saturation invariants.
func engageBlockedPool(t *testing.T, shardMax, queueSize int, idleLifetime time.Duration) (*WorkerPool[int], chan struct{}, func()) {
	t.Helper()

	release := make(chan struct{})
	var unblock sync.Once
	releaseAll := func() { unblock.Do(func() { close(release) }) }

	var running int32
	wp := NewWorkerPool(func(task int) {
		atomic.AddInt32(&running, 1)
		<-release
	})
	wp.SetNumShards(1)
	wp.SetShardMinWorkers(1)
	wp.SetShardMaxWorkers(shardMax)
	wp.SetQueueSize(queueSize)
	wp.SetIdleWorkerLifetime(idleLifetime)
	wp.Start()

	// Feed shardMax priming tasks so that one worker per task is engaged.
	// trySpawnWorker is triggered on the first enqueue (queue non-empty after
	// send), bringing shard.workers up to shardMax.
	for i := 0; i < shardMax; i++ {
		if err := wp.AddTask(-1 - i); err != nil {
			releaseAll()
			wp.Stop()
			t.Fatalf("priming AddTask %d: %v", i, err)
		}
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&running) < int32(shardMax) {
		time.Sleep(time.Millisecond)
	}
	if got := atomic.LoadInt32(&running); got != int32(shardMax) {
		releaseAll()
		wp.Stop()
		t.Fatalf("workers never fully engaged: running=%d, want %d", got, shardMax)
	}

	return wp, release, releaseAll
}

func TestAddTaskOverload(t *testing.T) {
	const queueSize = 16
	const shardMax = 2

	wp, _, releaseAll := engageBlockedPool(t, shardMax, queueSize, time.Hour)
	defer wp.Stop()
	defer releaseAll()

	// Both workers are blocked in handler. Fill the ring buffer to capacity.
	for i := 0; i < queueSize; i++ {
		if err := wp.AddTask(i); err != nil {
			t.Fatalf("AddTask buffer fill %d: %v", i, err)
		}
	}

	// Pool is saturated: shardMax workers blocked + queueSize queued. AddTask
	// must report overload immediately, repeatedly, and without blocking.
	for i := 0; i < 5; i++ {
		start := time.Now()
		err := wp.AddTask(9999)
		if err != ErrPoolOverload {
			t.Errorf("AddTask on saturated pool (attempt %d): got %v, want ErrPoolOverload", i, err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
			t.Errorf("AddTask blocked for %v on saturated pool; expected immediate return", elapsed)
		}
	}
}

func TestAddTaskWithBlockingWaits(t *testing.T) {
	const queueSize = 16
	const shardMax = 2

	// idleWorkerLifetime is long enough that the engagement phase can't race
	// against an above-floor worker retiring, but short enough that — once
	// release is closed — an excess worker idles out and fires notifyWaiter,
	// which is what unblocks AddTaskWithBlocking.
	wp, _, releaseAll := engageBlockedPool(t, shardMax, queueSize, 200*time.Millisecond)
	defer wp.Stop()
	defer releaseAll()

	// Fill the ring buffer to fully saturate the pool.
	for i := 0; i < queueSize; i++ {
		if err := wp.AddTask(i); err != nil {
			t.Fatalf("AddTask buffer fill %d: %v", i, err)
		}
	}
	if err := wp.AddTask(9999); err != ErrPoolOverload {
		t.Fatalf("pool not saturated: AddTask returned %v, want ErrPoolOverload", err)
	}

	// AddTaskWithBlocking on a saturated pool must NOT return ErrPoolOverload
	// and must NOT return early — it must wait for capacity.
	blockingDone := make(chan error, 1)
	go func() {
		blockingDone <- wp.AddTaskWithBlocking(1234)
	}()

	select {
	case err := <-blockingDone:
		t.Fatalf("AddTaskWithBlocking returned early on saturated pool: err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}

	// Relieve pressure: handlers complete, queue drains, an excess worker
	// retires past shardMin → notifyWaiter fires → blocked caller retries
	// AddTask successfully against an empty queue.
	releaseAll()

	select {
	case err := <-blockingDone:
		if err != nil {
			t.Errorf("AddTaskWithBlocking after release: got err %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AddTaskWithBlocking never unblocked after pressure was relieved")
	}
}

func TestTaskCompletenessAcrossConfigs(t *testing.T) {
	tests := []struct {
		name             string
		shards           int
		maxWorkersPerShd int
		tasksPerShard    int
	}{
		{"1 shard / 10 workers / 100 tasks", 1, 10, 100},
		{"2 shards / 20 workers / 200 tasks", 2, 10, 100},
		{"3 shards / 30 workers / 300 tasks", 3, 10, 100},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			totalTasks := tt.shards * tt.tasksPerShard

			var processed int64
			seen := make([]int32, totalTasks)

			var wg sync.WaitGroup
			wg.Add(totalTasks)

			wp := NewWorkerPool(func(task int) {
				// Simulate light work and verify each task is delivered exactly once.
				time.Sleep(time.Millisecond)
				if atomic.AddInt32(&seen[task], 1) != 1 {
					t.Errorf("task %d delivered more than once", task)
				}
				atomic.AddInt64(&processed, 1)
				wg.Done()
			})
			wp.SetNumShards(tt.shards)
			wp.SetShardMinWorkers(2)
			wp.SetShardMaxWorkers(tt.maxWorkersPerShd)
			wp.SetMaxWorkers(tt.shards * tt.maxWorkersPerShd)
			wp.SetIdleWorkerLifetime(50 * time.Millisecond)
			wp.Start()
			defer wp.Stop()

			for i := 0; i < totalTasks; i++ {
				if err := wp.AddTaskWithBlocking(i); err != nil {
					t.Fatalf("AddTaskWithBlocking(%d): %v", i, err)
				}
			}

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatalf("timed out: only %d/%d tasks completed", atomic.LoadInt64(&processed), totalTasks)
			}

			if got := atomic.LoadInt64(&processed); got != int64(totalTasks) {
				t.Errorf("processed count: got %d, want %d", got, totalTasks)
			}

			// Final exactness check: every task index seen exactly once.
			missing := 0
			for i, n := range seen {
				if atomic.LoadInt32(&seen[i]) != 1 {
					missing++
					if missing <= 5 {
						t.Errorf("task %d: delivered %d times, want 1", i, n)
					}
				}
			}
			if missing > 5 {
				t.Errorf("... and %d more tasks with incorrect delivery counts", missing-5)
			}
		})
	}
}

func TestShardWorkerFloor(t *testing.T) {
	const shards = 2
	const minWorkers = 3
	const maxWorkers = 10
	const burstTasks = 40
	const idleLifetime = 50 * time.Millisecond

	var wg sync.WaitGroup
	wg.Add(burstTasks)

	wp := NewWorkerPool(func(task int) {
		time.Sleep(20 * time.Millisecond)
		wg.Done()
	})
	wp.SetNumShards(shards)
	wp.SetShardMinWorkers(minWorkers)
	wp.SetShardMaxWorkers(maxWorkers)
	wp.SetIdleWorkerLifetime(idleLifetime)
	wp.Start()
	defer wp.Stop()

	// Burst tasks to push spawn above the floor on at least one shard.
	for i := 0; i < burstTasks; i++ {
		if err := wp.AddTaskWithBlocking(i); err != nil {
			t.Fatalf("AddTaskWithBlocking(%d): %v", i, err)
		}
	}

	wg.Wait()

	// Wait long enough for excess workers to retire via idle timeout.
	// The floor is per-shard; cumulative floor is minWorkers*shards.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if wp.GetSpawnedWorkers() == minWorkers*shards {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	got := wp.GetSpawnedWorkers()
	if got != minWorkers*shards {
		t.Errorf("after idle drain: spawned workers got %d, want exactly %d (floor must hold across all shards)", got, minWorkers*shards)
	}

	// Verify floor holds steady — no further attrition below the floor.
	time.Sleep(3 * idleLifetime)
	if got := wp.GetSpawnedWorkers(); got != minWorkers*shards {
		t.Errorf("floor drifted: spawned workers got %d, want %d", got, minWorkers*shards)
	}
}

func TestWorkerScaling(t *testing.T) {
	const shards = 1
	const shardMax = 5
	const sleepTime = 100 * time.Millisecond
	const tolerance = 20 * time.Millisecond

	tests := []struct {
		name      string
		numTasks  int
		maxRounds int
	}{
		{"1 task", 1, 1},
		{"5 tasks", 5, 1},
		{"10 tasks", 10, 2},
		{"11 tasks", 11, 3},
		{"20 tasks", 20, 4},
		{"30 tasks", 30, 6},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			wg.Add(tt.numTasks)

			wp := NewWorkerPool(func(task int) {
				time.Sleep(sleepTime)
				wg.Done()
			})
			wp.SetNumShards(shards)
			wp.SetShardMaxWorkers(shardMax)
			wp.SetMaxWorkers(0)
			wp.SetIdleWorkerLifetime(100 * time.Millisecond)
			wp.Start()
			defer wp.Stop()

			start := time.Now()
			for i := 0; i < tt.numTasks; i++ {
				if err := wp.AddTask(i); err != nil {
					t.Fatalf("AddTask(%d): %v", i, err)
				}
			}

			done := make(chan struct{})
			go func() {
				wg.Wait()
				close(done)
			}()

			expected := sleepTime * time.Duration(tt.maxRounds)
			select {
			case <-done:
			case <-time.After(expected + tolerance):
				t.Fatalf("timed out: tasks did not complete within %v", expected+tolerance)
			}

			elapsed := time.Since(start)
			if elapsed < expected-tolerance || elapsed > expected+tolerance {
				t.Errorf("unexpected duration: got %v, want ~%v (±%v)", elapsed, expected, tolerance)
			}
		})
	}
}

func TestStopAndWait(t *testing.T) {
	const numTasks = 50
	const taskDuration = 50 * time.Millisecond

	var completed int64

	wp := NewWorkerPool(func(task int) {
		time.Sleep(taskDuration)
		atomic.AddInt64(&completed, 1)
	})
	wp.SetNumShards(2)
	wp.SetShardMaxWorkers(5)
	wp.SetIdleWorkerLifetime(time.Second)
	wp.Start()

	for i := 0; i < numTasks; i++ {
		if err := wp.AddTaskWithBlocking(i); err != nil {
			t.Fatalf("AddTaskWithBlocking(%d): %v", i, err)
		}
	}

	wp.StopAndWait()

	if got := atomic.LoadInt64(&completed); got != numTasks {
		t.Errorf("completed tasks: got %d, want %d", got, numTasks)
	}

	if got := wp.GetSpawnedWorkers(); got != 0 {
		t.Errorf("spawned workers after StopAndWait: got %d, want 0", got)
	}
}

func TestStopWithTimeoutSuccess(t *testing.T) {
	const numTasks = 10
	const taskDuration = 20 * time.Millisecond

	wp := NewWorkerPool(func(task int) {
		time.Sleep(taskDuration)
	})
	wp.SetNumShards(1)
	wp.SetShardMaxWorkers(10)
	wp.SetIdleWorkerLifetime(time.Second)
	wp.Start()

	for i := 0; i < numTasks; i++ {
		if err := wp.AddTask(i); err != nil {
			t.Fatalf("AddTask(%d): %v", i, err)
		}
	}

	if !wp.StopWithTimeout(2 * time.Second) {
		t.Fatal("StopWithTimeout returned false; expected all workers to exit in time")
	}

	if got := wp.GetSpawnedWorkers(); got != 0 {
		t.Errorf("spawned workers after stop: got %d, want 0", got)
	}
}

func TestStopWithTimeoutExpires(t *testing.T) {
	release := make(chan struct{})

	wp := NewWorkerPool(func(task int) {
		<-release
	})
	wp.SetNumShards(1)
	wp.SetShardMinWorkers(1)
	wp.SetShardMaxWorkers(2)
	wp.SetQueueSize(16)
	wp.Start()

	// Submit a task that blocks forever until we release it.
	if err := wp.AddTask(1); err != nil {
		t.Fatalf("AddTask: %v", err)
	}
	// Give the worker time to pick it up.
	time.Sleep(10 * time.Millisecond)

	if wp.StopWithTimeout(50 * time.Millisecond) {
		t.Fatal("StopWithTimeout returned true; expected timeout since task is blocked")
	}

	// Unblock and allow cleanup.
	close(release)
	<-wp.doneChan
}

func TestConcurrentSubmitDuringStop(t *testing.T) {
	const producers = 20
	const tasksPerProducer = 100

	wp := NewWorkerPool(func(task int) {
		time.Sleep(time.Millisecond)
	})
	wp.SetNumShards(4)
	wp.SetShardMaxWorkers(10)
	wp.SetQueueSize(64)
	wp.SetIdleWorkerLifetime(100 * time.Millisecond)
	wp.Start()

	var wg sync.WaitGroup
	wg.Add(producers)

	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < tasksPerProducer; i++ {
				_ = wp.AddTask(i)
			}
		}()
	}

	// Stop while producers are still submitting.
	time.Sleep(5 * time.Millisecond)
	wp.StopAndWait()

	wg.Wait()

	if got := wp.GetSpawnedWorkers(); got != 0 {
		t.Errorf("spawned workers after StopAndWait: got %d, want 0", got)
	}
}

func TestAddTaskAfterStop(t *testing.T) {
	wp := NewWorkerPool(func(task int) {})
	wp.SetNumShards(1)
	wp.SetShardMaxWorkers(2)
	wp.Start()

	wp.StopAndWait()

	err := wp.AddTask(42)
	if err != ErrPoolStopped {
		t.Fatalf("AddTask after stop: got %v, want ErrPoolStopped", err)
	}
}

func TestStopIdempotent(t *testing.T) {
	wp := NewWorkerPool(func(task int) {
		time.Sleep(10 * time.Millisecond)
	})
	wp.SetNumShards(2)
	wp.SetShardMaxWorkers(4)
	wp.Start()

	for i := 0; i < 10; i++ {
		_ = wp.AddTask(i)
	}

	// Multiple calls must not panic or deadlock.
	wp.StopAndWait()
	wp.StopAndWait()
	wp.Stop()
}

func TestHighConcurrencyStress(t *testing.T) {
	const producers = 100
	const tasksPerProducer = 100

	var completed int64

	wp := NewWorkerPool(func(task int) {
		atomic.AddInt64(&completed, 1)
	})
	wp.SetNumShards(4)
	wp.SetShardMaxWorkers(32)
	wp.SetQueueSize(256)
	wp.SetIdleWorkerLifetime(50 * time.Millisecond)
	wp.Start()

	var wg sync.WaitGroup
	wg.Add(producers)

	for p := 0; p < producers; p++ {
		go func() {
			defer wg.Done()
			for i := 0; i < tasksPerProducer; i++ {
				for {
					err := wp.AddTask(i)
					if err == nil {
						break
					}
					if err == ErrPoolOverload {
						runtime.Gosched()
						continue
					}
					return
				}
			}
		}()
	}

	wg.Wait()
	wp.StopAndWait()

	if got := atomic.LoadInt64(&completed); got != producers*tasksPerProducer {
		t.Errorf("completed tasks: got %d, want %d", got, producers*tasksPerProducer)
	}
}
