package scheduler

import "testing"

func TestDispatchBackpressure(t *testing.T) {
	s := &Scheduler{
		queues: []chan Task{make(chan Task, 1)},
		weight: []int{1},
	}
	if !s.Dispatch(Task{ID: "t1"}) {
		t.Fatal("first dispatch must succeed")
	}
	if s.Dispatch(Task{ID: "t2"}) {
		t.Fatal("second dispatch must hit backpressure")
	}
}
