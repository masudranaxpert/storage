package provision

import "testing"

func TestValidateToolName(t *testing.T) {
	valid := []string{"", "all", "core", "media", "aria2", "aria2c", "ffmpeg", "rclone", "ARIA2", " FFmpeg "}
	for _, name := range valid {
		if err := ValidateToolName(name); err != nil {
			t.Errorf("expected valid tool %q, got error: %v", name, err)
		}
	}

	invalid := []string{"curl; rm -rf /", "malicious_script", "bash", "cat", "python", "../etc/passwd"}
	for _, name := range invalid {
		if err := ValidateToolName(name); err == nil {
			t.Errorf("expected invalid tool %q to be rejected, got nil", name)
		}
	}
}
