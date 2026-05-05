package ffstream

import (
	"os"
	"time"
)

func WriteEndMarkerFromEnv() error {
	markerPath := os.Getenv("FFSTREAM_END_MARKER_FILE")
	if markerPath == "" {
		return nil
	}
	return os.WriteFile(markerPath, []byte(time.Now().UTC().Format(time.RFC3339Nano)+"\n"), 0o644)
}
