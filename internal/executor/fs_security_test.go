package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestGeneralLocalFileReadIsBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.log")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), maxReadSize+1024), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadFileWithExecutor(&LocalExecutor{}, path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != maxReadSize {
		t.Fatalf("read %d bytes, want bounded %d", len(data), maxReadSize)
	}
}
