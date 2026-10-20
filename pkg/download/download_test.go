package download

import (
	"os"
	"testing"
)

func TestTLSConfigDefaults(t *testing.T) {
	// 1. By default, TLS verification must be enabled (InsecureSkipVerify: false)
	os.Unsetenv("STREAM_INSECURE_TLS")
	cfg := tlsConfig()
	if cfg.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=false by default")
	}

	// 2. Opt-in via STREAM_INSECURE_TLS=true
	os.Setenv("STREAM_INSECURE_TLS", "true")
	defer os.Unsetenv("STREAM_INSECURE_TLS")

	cfgInsecure := tlsConfig()
	if !cfgInsecure.InsecureSkipVerify {
		t.Fatal("expected InsecureSkipVerify=true when STREAM_INSECURE_TLS=true")
	}
}
