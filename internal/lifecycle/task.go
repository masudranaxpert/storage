package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"stream/internal/model"
)

// TaskKind classifies why a lifecycle task was created.
type TaskKind string

const (
	TaskPromote TaskKind = "promote"
	TaskDemote  TaskKind = "demote"
	TaskExpire  TaskKind = "expire"
	TaskManual  TaskKind = "manual"
)

// TaskState represents the persisted phase of a relocation or expiration task.
type TaskState string

const (
	TaskQueued    TaskState = "queued"
	TaskCopying   TaskState = "copying"
	TaskVerifying TaskState = "verifying"
	TaskSwitching TaskState = "switching"
	TaskCleanup   TaskState = "cleanup"
	TaskDone      TaskState = "done"
	TaskFailed    TaskState = "failed"
	TaskCancelled TaskState = "cancelled"
)

// IsTerminal reports whether the task state is final.
func (s TaskState) IsTerminal() bool {
	return s == TaskDone || s == TaskFailed || s == TaskCancelled
}

// Task represents one relocation or expiration operation managed by the lifecycle executor.
type Task struct {
	ID        string          `json:"id"`
	Key       string          `json:"key"`
	Kind      TaskKind        `json:"kind"`
	EdgeID    string          `json:"edge_id,omitempty"`
	Reason    string          `json:"reason"`
	From      model.Placement `json:"from"`
	To        model.Placement `json:"to"`
	State     TaskState         `json:"state"`
	Attempts  int               `json:"attempts"`
	Error     string            `json:"error,omitempty"`
	Progress  float64           `json:"progress"`
	Speed     string            `json:"speed,omitempty"`
	Bytes     int64             `json:"bytes"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	StartedAt time.Time         `json:"started_at,omitempty"`
}

// ProgressPayload is the JSON body posted by an agent to /api/lifecycle/tasks/{id}/progress.
type ProgressPayload struct {
	State            string  `json:"state"`
	ProgressPercent  float64 `json:"progress_percent"`
	Speed            string  `json:"speed"`
	Error            string  `json:"error"`
	TransferredBytes int64   `json:"transferred_bytes"`
	TotalBytes       int64   `json:"total_bytes"`
}

// NewTaskID generates a unique task identifier ("lct_" + 12 hex chars).
func NewTaskID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("lct_%012x", time.Now().UnixNano()&0xffffffffffff)
	}
	return "lct_" + hex.EncodeToString(b[:])
}

// AuditEntry logs a permanent file deletion triggered by an expire rule.
type AuditEntry struct {
	Key      string    `json:"key"`
	Filename string    `json:"filename"`
	Size     int64     `json:"size"`
	Tier     int       `json:"tier"`
	Node     string    `json:"node"`
	Path     string    `json:"path"`
	EdgeID   string    `json:"edge_id"`
	Reason   string    `json:"reason"`
	At       time.Time `json:"at"`
}
