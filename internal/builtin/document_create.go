package builtin

import (
	"ai-edr/internal/artifacts"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DocumentCreate writes new controller-side artifacts only. It cannot replace
// an existing user file, execute template code, or install dependencies.
func DocumentCreate(filePath, format, specJSON string) (string, error) {
	if !filepath.IsAbs(filePath) {
		return "", fmt.Errorf("path 必须为控制端绝对路径")
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(filePath)), ".")
	if format == "" {
		format = ext
	}
	format = strings.ToLower(format)
	if format != ext {
		return "", fmt.Errorf("format 与文件扩展名必须一致")
	}
	spec, err := artifacts.ParseSpec(specJSON)
	if err != nil {
		return "", fmt.Errorf("文档 spec 无效: %w", err)
	}
	data, warnings, err := artifacts.Create(format, spec)
	if err != nil {
		return "", err
	}
	// Reparse before publishing: malformed containers never become final files.
	parsed, err := parseDocumentBytes(filePath, data, 4000, 20, 10)
	if err != nil {
		return "", fmt.Errorf("生成结果回读失败: %w", err)
	}
	if strings.TrimSpace(parsed.Text) == "" && len(parsed.Tables) == 0 {
		return "", fmt.Errorf("生成文件未能提取正文/表格，拒绝发布空文件")
	}
	root, err := os.OpenRoot(filepath.Dir(filePath))
	if err != nil {
		return "", fmt.Errorf("输出目录不存在或不可访问: %w", err)
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Base(filePath), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("创建失败（不会覆盖已有文件）: %w", err)
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		_ = root.Remove(filepath.Base(filePath))
		return "", err
	}
	if err = f.Close(); err != nil {
		_ = root.Remove(filepath.Base(filePath))
		return "", err
	}
	digest := sha256.Sum256(data)
	raw, _ := json.Marshal(map[string]any{"path": filePath, "format": format, "bytes": len(data), "sha256": fmt.Sprintf("%x", digest), "validation": "已回读正文/表格；尚未替代 Office/PDF 视觉版式验收", "metadata": parsed.Meta, "warnings": warnings, "text_preview": truncateDocumentText(parsed.Text, 1500)})
	return string(raw), nil
}
