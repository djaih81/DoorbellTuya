// (c) go2rtc; lifecycle fixes in the downstream fork.
package utils

import "sync"

type Waiter struct {
	mu       sync.Mutex
	pending  int
	finished bool
	err      error
	done     chan struct{}
}

func (w *Waiter) init() {
	if w.done == nil {
		w.done = make(chan struct{})
	}
}

func (w *Waiter) Add(delta int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return
	}
	w.init()
	if w.pending+delta < 0 {
		panic("negative Waiter count")
	}
	w.pending += delta
}

func (w *Waiter) Wait() error {
	w.mu.Lock()
	w.init()
	done := w.done
	w.mu.Unlock()
	<-done
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.err
}

func (w *Waiter) Done(err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.finished {
		return
	}
	w.init()
	if w.pending > 0 {
		w.pending--
	}
	if w.pending == 0 {
		w.err = err
		w.finished = true
		close(w.done)
	}
}

func (w *Waiter) WaitChan() <-chan error {
	ch := make(chan error, 1)
	w.mu.Lock()
	w.init()
	if w.finished {
		ch <- w.err
		close(ch)
		w.mu.Unlock()
		return ch
	}
	w.mu.Unlock()
	go func() { ch <- w.Wait(); close(ch) }()
	return ch
}
