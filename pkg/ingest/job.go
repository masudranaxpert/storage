package ingest

import (
	"sort"
	"sync"
	"time"

	"stream/pkg/media"
)

// JobStatus represents the state of a video ingest and packaging job.
type JobStatus string

const (
	StatusQueued      JobStatus = "queued"
	StatusDownloading JobStatus = "downloading"
	StatusPackaging   JobStatus = "packaging"
	StatusDistributing JobStatus = "distributing"
	StatusCompleted   JobStatus = "completed"
	StatusFailed      JobStatus = "failed"
)

// IngestJob defines a distributed video download and CMAF packaging task.
type IngestJob struct {
	JobID           string              `json:"job_id"`
	SourceURL       string              `json:"source_url"`
	AssignedNodeID  string              `json:"assigned_node_id"`
	Status          JobStatus           `json:"status"`
	ProgressPercent float64             `json:"progress_percent"`
	DownloadSpeed   string              `json:"download_speed"`
	ErrorMsg        string              `json:"error_msg,omitempty"`
	CMAF            *media.CMAFPackage  `json:"cmaf,omitempty"`
	CreatedAt       time.Time           `json:"created_at"`
	UpdatedAt       time.Time           `json:"updated_at"`
}

// IngestQueue manages thread-safe tracking of active and historical ingest tasks.
type IngestQueue struct {
	mu   sync.RWMutex
	jobs map[string]*IngestJob
}

// NewQueue initializes an in-memory job registry.
func NewQueue() *IngestQueue {
	return &IngestQueue{
		jobs: make(map[string]*IngestJob),
	}
}

// Add inserts a new job into the queue.
func (q *IngestQueue) Add(job *IngestJob) {
	q.mu.Lock()
	defer q.mu.Unlock()
	job.UpdatedAt = time.Now()
	q.jobs[job.JobID] = job
}

// Get retrieves a job by ID.
func (q *IngestQueue) Get(jobID string) (*IngestJob, bool) {
	q.mu.RLock()
	defer q.mu.RUnlock()
	job, exists := q.jobs[jobID]
	if !exists {
		return nil, false
	}
	copied := *job
	return &copied, true
}

// UpdateStatus updates the progress, status, and error message of a job.
func (q *IngestQueue) UpdateStatus(jobID string, status JobStatus, progress float64, speed, errMsg string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job, exists := q.jobs[jobID]; exists {
		job.Status = status
		job.ProgressPercent = progress
		job.DownloadSpeed = speed
		job.ErrorMsg = errMsg
		job.UpdatedAt = time.Now()
	}
}

// SetCMAF attaches the finished CMAF package to the completed job.
func (q *IngestQueue) SetCMAF(jobID string, pkg *media.CMAFPackage) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if job, exists := q.jobs[jobID]; exists {
		job.CMAF = pkg
		job.Status = StatusCompleted
		job.ProgressPercent = 100.0
		job.UpdatedAt = time.Now()
	}
}

// List returns all registered ingest jobs in deterministic chronological order (newest first).
func (q *IngestQueue) List() []*IngestJob {
	q.mu.RLock()
	defer q.mu.RUnlock()
	list := make([]*IngestJob, 0, len(q.jobs))
	for _, j := range q.jobs {
		copied := *j
		list = append(list, &copied)
	}

	sort.Slice(list, func(i, j int) bool {
		if list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].JobID > list[j].JobID
		}
		return list[i].CreatedAt.After(list[j].CreatedAt)
	})

	return list
}

// Delete removes a job from the queue.
func (q *IngestQueue) Delete(jobID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.jobs, jobID)
}
