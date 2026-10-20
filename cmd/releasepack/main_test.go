package main

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPackUnicodeManifestOnlyAndRollback(t *testing.T) {
	root := t.TempDir()
	data := []byte("中文说明与实际数据")
	name := "验收说明.md"
	if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(root, "config.yaml"), []byte("private test configuration"), 0600)
	_ = os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(fmt.Sprintf("%x  ./%s\n", sha256.Sum256(data), name)), 0644)
	if err := pack(root, "release.zip"); err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(filepath.Join(root, "release.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	if len(z.File) != 2 || z.File[1].Name != name || z.File[1].Flags&0x800 == 0 {
		t.Fatal("Unicode path flags missing or unlisted private config packed")
	}
	before, _ := os.ReadFile(filepath.Join(root, "release.zip"))
	_ = os.WriteFile(filepath.Join(root, name), []byte("changed since manifest"), 0644)
	if err := pack(root, "release.zip"); err == nil {
		t.Fatal("accepted changed artifact")
	}
	after, _ := os.ReadFile(filepath.Join(root, "release.zip"))
	if string(before) != string(after) {
		t.Fatal("failed build replaced previous valid archive")
	}
}
