package model

import (
	"time"
)

// JobStore persists file records and jobs (implemented by database.Store).
type JobStore interface {
	SaveFileRecord(rec *FileRecord) error
	GetFileRecord(key string) (*FileRecord, error)
	FileRecordExists(key string) (bool, error)
	DeleteFileRecord(key string) error
	ListFileRecords() ([]*FileRecord, error)
	SaveFileJob(job *FileJob) error
	GetFileJob(key string) (*FileJob, error)
	DeleteFileJob(key string) error
	ListFileJobs() ([]*FileJob, error)
}

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
	Key        string     `json:"key"` // primary key
	Filename   string     `json:"filename"`
	SizeBytes  int64      `json:"size_bytes"`
	MimeType   string     `json:"mime_type"`
	SourceType SourceType `json:"source_type"`
	CreatedBy  string     `json:"created_by,omitempty"` // API key name or "Dashboard"
	CreatedAt  time.Time  `json:"created_at"`
}

// FileJob is the job table: lifecycle + placement for one FileRecord.
type FileJob struct {
	Key              string    `json:"key"` // FK -> FileRecord.Key
	State            FileState `json:"state"`
	SourceURL        string    `json:"source_url,omitempty"`
	NodeID           string    `json:"node_id,omitempty"` // assigned processing worker
	Placement        Placement `json:"placement,omitempty"`
	Progress         float64   `json:"progress_percent"`
	Speed            string    `json:"speed,omitempty"`
	Error            string    `json:"error,omitempty"`
	Stage            string    `json:"stage,omitempty"` // "upload" | "download" | "process" | "transfer" | "ready"
	StageName        string    `json:"stage_name,omitempty"`
	StagePercent     float64   `json:"stage_percent,omitempty"`
	TransferredBytes int64     `json:"transferred_bytes,omitempty"`
	TotalBytes       int64     `json:"total_bytes,omitempty"`
	ETA              string    `json:"eta,omitempty"`
	Details          string    `json:"details,omitempty"`
	Attempts         int       `json:"attempts"`
	MetadataJSON     []byte    `json:"metadata_json,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
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
	CreatedBy    string `json:"created_by,omitempty"`
}

// CreateResponse is the master's reply for both modes. UploadURL is only
// set for upload mode.
type CreateResponse struct {
	Key       string    `json:"key"`
	State     FileState `json:"state"`
	Filename  string    `json:"filename,omitempty"`
	SizeBytes int64     `json:"size_bytes,omitempty"`
	MimeType  string    `json:"mime_type,omitempty"`
	CreatedBy string    `json:"created_by,omitempty"`
	UploadURL string    `json:"upload_url,omitempty"`
	Placement Placement `json:"placement,omitempty"`
	StatusURL string    `json:"status_url"`
	StreamURL string    `json:"stream_url,omitempty"`
	PlayURL   string    `json:"play_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// StatusResponse is the GET /api/v1/files/{key} reply.
type StatusResponse struct {
	Key              string                 `json:"key"`
	State            FileState              `json:"state"`
	Filename         string                 `json:"filename,omitempty"`
	SizeBytes        int64                  `json:"size_bytes,omitempty"`
	MimeType         string                 `json:"mime_type,omitempty"`
	CreatedBy        string                 `json:"created_by,omitempty"`
	Progress         float64                `json:"progress_percent"`
	Speed            string                 `json:"speed,omitempty"`
	Error            string                 `json:"error,omitempty"`
	Stage            string                 `json:"stage,omitempty"`
	StageName        string                 `json:"stage_name,omitempty"`
	StagePercent     float64                `json:"stage_percent,omitempty"`
	TransferredBytes int64                  `json:"transferred_bytes,omitempty"`
	TotalBytes       int64                  `json:"total_bytes,omitempty"`
	ETA              string                 `json:"eta,omitempty"`
	Details          string                 `json:"details,omitempty"`
	Source           SourceType             `json:"source_type"`
	NodeID           string                 `json:"node_id,omitempty"`        // processing worker node
	WorkerNodeID     string                 `json:"worker_node_id,omitempty"` // node running download/remux
	Placement        Placement              `json:"placement,omitempty"`      // final storage block owner
	StreamURL        string                 `json:"stream_url,omitempty"`
	PlayURL          string                 `json:"play_url,omitempty"`
	Metadata         map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt        time.Time              `json:"created_at"`
	UpdatedAt        time.Time              `json:"updated_at"`
}

// S3Config wraps connection parameters for an S3/R2 remote bucket.
type S3Config struct {
	Name       string `json:"name"`
	Provider   string `json:"provider"`
	Endpoint   string `json:"endpoint"`
	Region     string `json:"region"`
	AccessKey  string `json:"access_key"`
	SecretKey  string `json:"secret_key"`
	PublicHost string `json:"public_host,omitempty"`
}

// RelocateRequest is the JSON payload accepted by POST /api/v1/relocate.
type RelocateRequest struct {
	TaskID      string    `json:"task_id"`
	Key         string    `json:"key"`
	SrcDir      string    `json:"src_dir"`
	FinalNodeID string    `json:"final_node_id"`
	FinalDir    string    `json:"final_dir"`
	FinalAddr   string    `json:"final_addr"`
	ProgressURL string    `json:"progress_url"`
	S3          *S3Config `json:"s3,omitempty"`
	SrcS3       *S3Config `json:"src_s3,omitempty"`
}

// MediaSizeResponse is the JSON payload returned by GET /api/v1/media-size.
type MediaSizeResponse struct {
	Bytes int64 `json:"bytes"`
	Files int   `json:"files"`
}

// StreamDriveStat reports live storage directory usage for an agent drive.
type StreamDriveStat struct {
	DriveName           string `json:"drive_name"`
	MountPoint          string `json:"mount_point"`
	StreamRoot          string `json:"stream_root"`
	MediaDir            string `json:"media_dir"`
	MediaSizeBytes      uint64 `json:"media_size_bytes"`
	MediaFileCount      int    `json:"media_file_count"`
	ProcessingDir       string `json:"processing_dir"`
	ProcessingSizeBytes uint64 `json:"processing_size_bytes"`
	ProcessingFileCount int    `json:"processing_file_count"`
	TotalStreamBytes    uint64 `json:"total_stream_bytes"`
}

