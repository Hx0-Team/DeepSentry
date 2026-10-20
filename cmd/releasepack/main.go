// releasepack packages the explicit public manifest with UTF-8 ZIP names.
package main

import (
	"archive/zip"
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	root := flag.String("root", "build", "release artifact directory")
	name := flag.String("out", "", "archive basename")
	flag.Parse()
	if err := pack(*root, *name); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func pack(root, name string) error {
	if name == "" || filepath.Base(name) != name {
		return fmt.Errorf("out must be a basename")
	}
	manifest, err := os.ReadFile(filepath.Join(root, "SHA256SUMS"))
	if err != nil {
		return err
	}
	entries := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(string(manifest)), "\n") {
		hash, path, ok := strings.Cut(line, "  ")
		path = strings.TrimPrefix(path, "./")
		if !ok || len(hash) != 64 || !filepath.IsLocal(path) || strings.Contains(path, "\\") {
			return fmt.Errorf("invalid manifest entry")
		}
		entries[path] = hash
	}
	entries["SHA256SUMS"] = fmt.Sprintf("%x", sha256.Sum256(manifest))
	f, err := os.CreateTemp(root, ".release-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	zw := zip.NewWriter(f)
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		hash := entries[path]
		file := filepath.Join(root, path)
		info, err := os.Lstat(file)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("release entry is not a regular file: %s", path)
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
			return fmt.Errorf("release file changed after hashing: %s", path)
		}
		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(path)
		header.Method = zip.Deflate
		writer, err := zw.CreateHeader(header)
		if err != nil {
			return err
		}
		if _, err = writer.Write(data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(root, name))
}
