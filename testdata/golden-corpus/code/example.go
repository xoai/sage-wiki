package scheduler

// Scheduler assigns work to worker pools using a weighted
// round-robin strategy with backpressure signals.
type Scheduler struct {
	queues []chan Task
	weight []int
}

// Task is a unit of schedulable work with a priority hint.
type Task struct {
	ID       string
	Priority int
	Payload  []byte
}

// Dispatch selects the next queue by weight and enqueues the task.
// It returns false when every queue is full (backpressure).
func (s *Scheduler) Dispatch(t Task) bool {
	for i, q := range s.queues {
		if s.weight[i] > 0 {
			select {
			case q <- t:
				return true
			default:
			}
		}
	}
	return false
}
