package contention

import (
	"sync"
	"sync/atomic"
	"time"
)

type BIncrStorage struct {
	// Хранилище для агрегации данных
	storage Counter

	// Число потоков, пишущих в хранилище
	writersCount int

	// Каждый писатель имеет пару контейнеров для предварительной
	// агрегации данных. Писатель пишет в один контейнер отдельная
	// горутина применяет второй контейнер к основному хранилищу
	batches [][2]map[Key]int64

	// Номер активного контейнера для записи
	writeBatch int32

	// Количество активных писателей
	activeWriters int32
	// Флаг синхронизации для переключения активного контейнера
	swapLock int32

	// Интервал предварительной агрегации
	swapInterval time.Duration

	batchGen int64 //debug
}

func NewBIncrStorage(wc int, swapInterval time.Duration, counter Counter) *BIncrStorage {
	r := &BIncrStorage{
		writersCount: wc,
		writeBatch:   0,
		batches:      make([][2]map[Key]int64, 0, wc),
		swapInterval: swapInterval,
		storage:      counter,
	}
	for i := 0; i < wc; i++ {
		r.batches = append(r.batches, [2]map[Key]int64{make(map[Key]int64), make(map[Key]int64)})
	}
	go r.swapAndApplyBatch()
	return r
}

func (s *BIncrStorage) applyBatchToStorage(readBatch int32) {
	for wn := 0; wn < s.writersCount; wn++ {
		for k, v := range s.batches[wn][readBatch] {
			m := Message{
				Key:   k,
				Value: v,
			}
			s.storage.Apply(m, wn)
			delete(s.batches[wn][readBatch], k)
		}
	}
}

func (s *BIncrStorage) swapAndApplyBatch() {
	for {
		time.Sleep(s.swapInterval)

		atomic.StoreInt32(&s.swapLock, 1)
		// wait for all pending readers
		for {
			if atomic.LoadInt32(&s.activeWriters) == 0 {
				break
			}
		}

		//swap batch
		readBatch := atomic.LoadInt32(&s.writeBatch)
		atomic.StoreInt32(&s.writeBatch, (readBatch+1)&1)

		atomic.StoreInt32(&s.swapLock, 0)

		atomic.AddInt64(&s.batchGen, 1) //debug

		//apply batch to main storage
		s.applyBatchToStorage(readBatch)
	}
}

func (s *BIncrStorage) BatchGeneration() int64 {
	return atomic.LoadInt64(&s.batchGen)
}

func (s *BIncrStorage) Consume(messages chan Message) {
	wg := sync.WaitGroup{}
	for i := 0; i < s.writersCount; i++ {
		wg.Add(1)
		go func(n int) {
			for m := range messages {
				s.Apply(m, n)
			}
			wg.Done()
		}(i)
	}
	wg.Wait()
}

//go:nosplit
func (s *BIncrStorage) Apply(msg Message, wn int) {

lock:
	atomic.AddInt32(&s.activeWriters, 1)
	holdLock := true
	for {
		if atomic.LoadInt32(&s.swapLock) == 0 {
			break
		}
		if holdLock {
			holdLock = false
			atomic.AddInt32(&s.activeWriters, -1)
		}
	}
	if !holdLock {
		goto lock
	}

	writeBatch := atomic.LoadInt32(&s.writeBatch)
	s.batches[wn][writeBatch][msg.Key] += msg.Value

	atomic.AddInt32(&s.activeWriters, -1)
}

func (s *BIncrStorage) Get(k Key) int64 {
	r := s.storage.Get(k)
	return r
}
