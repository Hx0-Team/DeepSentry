package builtin

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Uploaded/generated documents routinely live in reports/chat attachments.
// Generic forensic readers intentionally block that tree; document readers
// admit supported document types while still excluding configuration backups.
func (rt Runtime) readDocumentTargetLimited(name string) ([]byte, error) {
	lower := strings.ToLower(filepath.Clean(name))
	if strings.Contains(lower, ".deepsentry_backups") || strings.Contains(lower, "config.yaml") || strings.Contains(lower, "config.yml") {
		return nil, fmt.Errorf("拒绝读取DeepSentry配置/备份")
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".xlsx", ".xlsm", ".pptx", ".doc", ".xls", ".ppt", ".csv", ".tsv", ".rtf":
	default:
		return nil, fmt.Errorf("不支持的文档类型；不能使用 document_parse 读取任意配置或二进制")
	}
	ex := rt.executor()
	if ex == nil || !ex.IsRemote() {
		f, err := os.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, maxDocumentParseRead+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxDocumentParseRead {
			return nil, fmt.Errorf("文档超过20MiB")
		}
		return data, nil
	}
	data, err := ex.ReadTargetFile(name)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentParseRead {
		return nil, fmt.Errorf("文档超过20MiB")
	}
	return data, nil
}

type documentTextBuffer struct {
	bytes.Buffer
	limit int
}

func (b *documentTextBuffer) Write(data []byte) (int, error) {
	if b.Len()+len(data) > b.limit {
		return 0, fmt.Errorf("PDF提取正文超过4MiB")
	}
	return b.Buffer.Write(data)
}

func popplerPDFText(data []byte) (string, error) {
	program, err := exec.LookPath("pdftotext")
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, "-layout", "-enc", "UTF-8", "-", "-")
	cmd.Stdin = bytes.NewReader(data)
	out := &documentTextBuffer{limit: 4 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

func pdfLayoutWithPages(text string) string {
	var b strings.Builder
	pages := strings.Split(text, "\f")
	for index, page := range pages {
		if index == len(pages)-1 && strings.TrimSpace(page) == "" {
			continue // pdftotext ends the last page with a form feed.
		}
		fmt.Fprintf(&b, "[PDF page %d]\n%s\n", index+1, page)
	}
	return b.String()
}
