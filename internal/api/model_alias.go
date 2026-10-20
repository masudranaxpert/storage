package api

import (
	"stream/internal/model"
)

type FileRecord = model.FileRecord
type FileJob = model.FileJob
type FileState = model.FileState
type Placement = model.Placement
type ProgressUpdate = model.ProgressUpdate
type JobStore = model.JobStore
type SourceType = model.SourceType
type CreateRequest = model.CreateRequest
type CreateResponse = model.CreateResponse
type StatusResponse = model.StatusResponse
type S3Config = model.S3Config
type RelocateRequest = model.RelocateRequest
type MediaSizeResponse = model.MediaSizeResponse
type StreamDriveStat = model.StreamDriveStat

const (
	StateDetected       = model.StateDetected
	StateAwaitingUpload = model.StateAwaitingUpload
	StateUploading      = model.StateUploading
	StateDownloading    = model.StateDownloading
	StateProcessing     = model.StateProcessing
	StateTransferring   = model.StateTransferring
	StateCompleted      = model.StateCompleted
	StateFailed         = model.StateFailed

	SourceURL    = model.SourceURL
	SourceUpload = model.SourceUpload
)
