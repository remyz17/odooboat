package ui

import (
	"sync"
	"time"

	"github.com/remyz17/odooboat/internal/app"
)

// Queue stands between reporters and a sink: Report never waits on
// rendering, and one goroutine hands records to the sink in order, so a
// sink is never called concurrently (ADR 0011 §4).
type Queue struct {
	sink Sink
	now  func() time.Time

	mu      sync.Mutex
	wake    *sync.Cond
	records []Record
	closed  bool
	stopped chan struct{}
}

// flushMark is queued by Flush; the drainer closes it when it reaches it.
type flushMark chan struct{}

// NewQueue starts draining into sink; now stamps reported events.
func NewQueue(sink Sink, now func() time.Time) *Queue {
	q := &Queue{sink: sink, now: now, stopped: make(chan struct{})}
	q.wake = sync.NewCond(&q.mu)
	go q.drain()
	return q
}

// Report queues e with the current time. It implements app.Reporter.
func (q *Queue) Report(e app.Event) { q.Post(Record{Time: q.now(), Event: e}) }

// Post queues a prepared record. A Transfer replaces a queued Transfer of
// the same step when that one is last in line; nothing else is coalesced.
func (q *Queue) Post(r Record) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	if t, ok := r.Event.(app.Transfer); ok && len(q.records) > 0 {
		if last, ok := q.records[len(q.records)-1].Event.(app.Transfer); ok && last.ID == t.ID {
			q.records[len(q.records)-1] = r
			return
		}
	}
	q.records = append(q.records, r)
	q.wake.Signal()
}

// Flush returns once every record posted before the call has been handled.
func (q *Queue) Flush() {
	mark := make(flushMark)
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.records = append(q.records, Record{Event: mark})
	q.wake.Signal()
	q.mu.Unlock()
	<-mark
}

// Close flushes, then stops the queue; later reports are dropped.
func (q *Queue) Close() {
	q.Flush()
	q.mu.Lock()
	if !q.closed {
		q.closed = true
		q.wake.Signal()
	}
	q.mu.Unlock()
	<-q.stopped
}

// pending is the number of records not yet taken by the drainer.
func (q *Queue) pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.records)
}

func (q *Queue) drain() {
	defer close(q.stopped)
	for {
		q.mu.Lock()
		for len(q.records) == 0 && !q.closed {
			q.wake.Wait()
		}
		if len(q.records) == 0 {
			q.mu.Unlock()
			return
		}
		r := q.records[0]
		q.records[0] = Record{}
		q.records = q.records[1:]
		q.mu.Unlock()
		if mark, ok := r.Event.(flushMark); ok {
			close(mark)
			continue
		}
		q.sink.Handle(r)
	}
}
