package media

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// MediaFolderStructure defines the standardized isolated directory layout for any video.
type MediaFolderStructure struct {
	MediaID      string `json:"media_id"`
	BaseDir      string `json:"base_dir"`
	RawDir       string `json:"raw_dir"`
	RawFilePath  string `json:"raw_file_path"`
	MetadataPath string `json:"metadata_path"`
}

// PrepareMediaFolder creates a dedicated, isolated folder for a given media ID and filename.
// The HLS package and metadata.json land directly at the root of <baseDir>.
func PrepareMediaFolder(rootDataDir, mediaID, originalFilename string) (*MediaFolderStructure, error) {
	if rootDataDir == "" {
		rootDataDir = filepath.Join("data", "media")
	}

	cleanFilename := SanitizeFilename(originalFilename)
	if cleanFilename == "" {
		cleanFilename = "video.mp4"
	}

	baseDir := filepath.Join(rootDataDir, mediaID)
	rawDir := filepath.Join(baseDir, "raw")

	if err := os.MkdirAll(rawDir, 0755); err != nil {
		return nil, fmt.Errorf("create raw dir: %w", err)
	}

	return &MediaFolderStructure{
		MediaID:      mediaID,
		BaseDir:      baseDir,
		RawDir:       rawDir,
		RawFilePath:  filepath.Join(rawDir, cleanFilename),
		MetadataPath: filepath.Join(baseDir, "metadata.json"),
	}, nil
}

// SaveMetadata writes the media specifications to metadata.json inside the media folder.
func (m *MediaFolderStructure) SaveMetadata(meta any) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	return os.WriteFile(m.MetadataPath, data, 0644)
}

// CleanRaw deletes the temporary raw upload/download directory to reclaim 100% redundant storage.
func (m *MediaFolderStructure) CleanRaw() error {
	return os.RemoveAll(m.RawDir)
}
