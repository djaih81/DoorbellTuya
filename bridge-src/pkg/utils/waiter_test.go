package utils

import (
	"errors"
	"testing"
	"time"
)

func TestWaitChanAfterCompletion(t *testing.T) {
	for _, result := range []error{nil, errors.New("handshake rejected")} {
		var waiter Waiter
		waiter.Done(result) // The MQTT callback may run before Start starts waiting.
		for i := 0; i < 3; i++ {
			select {
			case got := <-waiter.WaitChan():
				if got != result {
					t.Fatalf("got %v, want %v", got, result)
				}
			case <-time.After(time.Second):
				t.Fatal("completed waiter returned a blocking channel")
			}
		}
	}
}

func TestAbandonedWaitChanDoesNotBlockCompletion(t *testing.T) {
	var waiter Waiter
	abandoned := waiter.WaitChan()
	waiter.Done(nil)
	waiter.Done(errors.New("duplicate disconnect"))
	select {
	case got := <-abandoned:
		if got != nil {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("completion was lost")
	}
}

func TestWaiterWaitsForAllAddedTasks(t *testing.T) {
	var waiter Waiter
	waiter.Add(2)
	result := waiter.WaitChan()
	waiter.Done(nil)
	select {
	case <-result:
		t.Fatal("completed before second task")
	default:
	}
	want := errors.New("second task failed")
	waiter.Done(want)
	if got := <-result; got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
