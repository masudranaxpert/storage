package storage

import (
	"stream/pkg/cluster"
)

// S3Config wraps connection parameters for an S3/R2 remote bucket.
type S3Config = cluster.S3Config

// S3Object represents a stored item in an S3/R2 bucket.
type S3Object = cluster.S3Object

var (
	TestBucketConnection = cluster.TestBucketConnection
	EnableBucketCORS     = cluster.EnableBucketCORS
	SignS3Request        = cluster.SignS3Request
	UploadObjectS3       = cluster.UploadObjectS3
	UploadStreamS3       = cluster.UploadStreamS3
	DownloadObjectS3     = cluster.DownloadObjectS3
	HeadObjectS3         = cluster.HeadObjectS3
	DeleteObjectS3       = cluster.DeleteObjectS3
	ListObjectsS3        = cluster.ListObjectsS3
	DeleteFolderS3       = cluster.DeleteFolderS3
	UploadFolderS3       = cluster.UploadFolderS3
	DownloadFolderS3     = cluster.DownloadFolderS3
)
