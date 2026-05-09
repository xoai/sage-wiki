package trust

import "time"

type OutputState string

const (
	StatePending   OutputState = "pending"
	StateConfirmed OutputState = "confirmed"
	StateConflict  OutputState = "conflict"
	StateStale     OutputState = "stale"
)

type PendingOutput struct {
	ID              string
	Question        string
	QuestionHash    string
	Answer          string
	AnswerHash      string
	State           OutputState
	Confirmations   int
	GroundingScore  *float64
	SourcesHash     string
	SourcesUsed     string // JSON array
	FilePath        string
	CreatedAt       time.Time
	PromotedAt      *time.Time
	DemotedAt       *time.Time
}

type Confirmation struct {
	ID          int
	OutputID    string
	ChunkIDs    string // JSON array
	AnswerHash  string
	ConfirmedAt time.Time
}
