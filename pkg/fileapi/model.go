// Package fileapi implements the public v1 file ingestion API: clients
// submit a video URL or request an upload slot, the master validates the
// source (HEAD-probe metadata, video MIME), and picks two targets
// independently: a storage block (Tier 2 HDD first, Tier 1 SSD overflow,
// never Tier 0 — the block may sit on ANY agent node, processing-enabled
// or storage-only) and a processing worker (tools-ready VPS that runs the
// download + CMAF remux in its own scratch space). When the worker also
// owns the chosen block the bytes land there directly; otherwise the
// worker streams the finished folder to the block owner (agent-to-agent
// transfer) and cleans its scratch. Every file gets a 16-character
// URL-safe key.
package fileapi

import (
	"time"
)

// FileState is the lifecycle of a file job.
type FileState string

const (
	// StateDetected: URL job validated and accepted, about to dispatch.
	StateDetected FileState = "detected"
	// StateAwaitingUpload: upload slot reserved, waiting for client bytes.
	StateAwaitingUpload FileState = "awaiting_upload"
	// StateUploading: client is currently streaming raw bytes to the cluster.
	StateUploading FileState = "uploading"
	// StateDownloading: the assigned worker is pulling the source.
	StateDownloading FileState = "downloading"
	// StateProcessing: download finished, video is being validated/remuxed.
	StateProcessing FileState = "processing"
	// StateTransferring: processing done; the worker is streaming the
	// finished folder to the block owner (cross-node placement).
	StateTransferring FileState = "transferring"
	// StateCompleted: stored and streamable.
	StateCompleted FileState = "completed"
	// StateFailed: terminal failure; Error carries the reason.
	StateFailed FileState = "failed"
)

// SourceType distinguishes URL ingest from direct client upload.
type SourceType string

const (
	SourceURL    SourceType = "url"
	SourceUpload SourceType = "upload"
)

// Placement records where a file lives (written once the block is chosen).
type Placement struct {
	TierID     int    `json:"tier_id"`
	TierLabel  string `json:"tier_label"`
	NodeID     string `json:"node_id"`
	Path       string `json:"path"`
	PublicHost string `json:"public_host,omitempty"`
}

// FileRecord is the main table: one row per stored file, keyed by the
// public 16-character key.
type FileRecord struct {
	Key        string    `json:"key"` // primary key
	Filename   string    `json:"filename"`
	SizeBytes  int64     `json:"size_bytes"`
	MimeType   string    `json:"mime_type"`
	SourceType SourceType `json:"source_type"`
	CreatedAt  time.Time `json:"created_at"`
}

// FileJob is the job table: lifecycle + placement for one FileRecord.
// FileJob.Key is the foreign key into the file table. NodeID is the
// processing worker executing the job; Placement is the final storage
// location — the owner of the block — which may be a different node when
// placement is decoupled (worker processes in scratch, then transfers).
type FileJob struct {
	Key              string     `json:"key"` // FK -> FileRecord.Key
	State            FileState  `json:"state"`
	SourceURL        string     `json:"source_url,omitempty"`
	NodeID           string     `json:"node_id,omitempty"` // assigned processing worker
	Placement        Placement  `json:"placement,omitempty"`
	Progress         float64    `json:"progress_percent"`
	Speed            string     `json:"speed,omitempty"`
	Error            string     `json:"error,omitempty"`
	Stage            string     `json:"stage,omitempty"` // "upload" | "download" | "process" | "transfer" | "ready"
	StageName        string     `json:"stage_name,omitempty"`
	StagePercent     float64    `json:"stage_percent,omitempty"`
	TransferredBytes int64      `json:"transferred_bytes,omitempty"`
	TotalBytes       int64                  `json:"total_bytes,omitempty"`
	ETA              string                 `json:"eta,omitempty"`
	Details          string                 `json:"details,omitempty"`
	Attempts         int                    `json:"attempts"`
	MetadataJSON     []byte                 `json:"metadata_json,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// ProgressUpdate is the wire shape workers POST back to the master.
type ProgressUpdate struct {
	State            FileState `json:"state"`
	Percent          float64   `json:"progress_percent"`
	Speed            string    `json:"speed,omitempty"`
	Error            string    `json:"error,omitempty"`
	Stage            string    `json:"stage,omitempty"`
	StageName        string    `json:"stage_name,omitempty"`
	StagePercent     float64   `json:"stage_percent,omitempty"`
	TransferredBytes int64     `json:"transferred_bytes,omitempty"`
	TotalBytes       int64     `json:"total_bytes,omitempty"`
	ETA              string    `json:"eta,omitempty"`
	Details          string    `json:"details,omitempty"`
	CMAFJSON         []byte    `json:"cmaf_json,omitempty"`
}

// CreateRequest is the POST /api/v1/files body: exactly one of URL / upload.
type CreateRequest struct {
	URL          string `json:"url,omitempty"`
	Filename     string `json:"filename,omitempty"`
	ExpectedSize int64  `json:"size_bytes,omitempty"` // upload mode hint
}

// CreateResponse is the master's reply for both modes. UploadURL is only
// set for upload mode.
type CreateResponse struct {
	Key        string    `json:"key"`
	State      FileState `json:"state"`
	Filename   string    `json:"filename,omitempty"`
	SizeBytes  int64     `json:"size_bytes,omitempty"`
	MimeType   string    `json:"mime_type,omitempty"`
	UploadURL  string    `json:"upload_url,omitempty"`
	Placement  Placement `json:"placement,omitempty"`
	StatusURL  string    `json:"status_url"`
	StreamURL  string    `json:"stream_url,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// StatusResponse is the GET /api/v1/files/{key} reply.
type StatusResponse struct {
	Key              string     `json:"key"`
	State            FileState  `json:"state"`
	Filename         string     `json:"filename,omitempty"`
	SizeBytes        int64      `json:"size_bytes,omitempty"`
	MimeType         string     `json:"mime_type,omitempty"`
	Progress         float64    `json:"progress_percent"`
	Speed            string     `json:"speed,omitempty"`
	Error            string     `json:"error,omitempty"`
	Stage            string     `json:"stage,omitempty"`
	StageName        string     `json:"stage_name,omitempty"`
	StagePercent     float64    `json:"stage_percent,omitempty"`
	TransferredBytes int64      `json:"transferred_bytes,omitempty"`
	TotalBytes       int64      `json:"total_bytes,omitempty"`
	ETA              string     `json:"eta,omitempty"`
	Details          string     `json:"details,omitempty"`
	Source           SourceType `json:"source_type"`
	WorkerNodeID     string                 `json:"worker_node_id,omitempty"` // node running download/remux
	Placement        Placement              `json:"placement,omitempty"`      // final storage block owner
	StreamURL        string                 `json:"stream_url,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}
