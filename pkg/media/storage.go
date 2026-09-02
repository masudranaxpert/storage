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
	CMAFDir      string `json:"cmaf_dir"`
	ManifestPath string `json:"manifest_path"`
	MetadataPath string `json:"metadata_path"`
}

// PrepareMediaFolder creates a dedicated, isolated folder for a given media ID and filename.
// This guarantees that all assets (raw download, CMAF video segments, audio tracks, manifests)
// for a single video remain encapsulated inside its own dedicated directory.
func PrepareMediaFolder(rootDataDir, mediaID, originalFilename string) (*MediaFolderStructure, error) {
	if rootDataDir == "" {
		rootDataDir = filepath.Join("data", "media")
	}

	cleanFilename := SanitizeFilename(originalFilename)
	baseDir := filepath.Join(rootDataDir, mediaID)
	rawDir := filepath.Join(baseDir, "raw")
	cmafDir := filepath.Join(baseDir, "cmaf")

	if err := os.MkdirAll(rawDir, 0755); err != nil {
		return nil, fmt.Errorf("create raw dir: %w", err)
	}
	if err := os.MkdirAll(cmafDir, 0755); err != nil {
		return nil, fmt.Errorf("create cmaf dir: %w", err)
	}

	return &MediaFolderStructure{
		MediaID:      mediaID,
		BaseDir:      baseDir,
		RawDir:       rawDir,
		RawFilePath:  filepath.Join(rawDir, cleanFilename),
		CMAFDir:      cmafDir,
		ManifestPath: filepath.Join(baseDir, "master.m3u8"),
		MetadataPath: filepath.Join(baseDir, "metadata.json"),
	}, nil
}

// SaveMetadata writes the packaged CMAF metadata to metadata.json inside the media folder.
func (m *MediaFolderStructure) SaveMetadata(pkg *CMAFPackage) error {
	data, err := json.MarshalIndent(pkg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	return os.WriteFile(m.MetadataPath, data, 0644)
}

// SaveHLSManifest writes the generated master.m3u8 to the root of the media folder.
func (m *MediaFolderStructure) SaveHLSManifest(manifestContent string) error {
	return os.WriteFile(m.ManifestPath, []byte(manifestContent), 0644)
}

// CleanRaw deletes the temporary raw upload/download directory to reclaim 100% redundant storage.
func (m *MediaFolderStructure) CleanRaw() error {
	return os.RemoveAll(m.RawDir)
}
