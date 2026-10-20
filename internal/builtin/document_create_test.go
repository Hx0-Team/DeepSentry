package builtin

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const reportSpec = `{"title":"月度安全报告","subtitle":"测试样本 · 2026年10月","author":"DeepSentry","sections":[{"heading":"总体情况","text":"本月核查两台资产。仅使用测试数据，不能作为真实安全结论。"},{"heading":"风险与建议","bullets":["修复已核验的风险","复查管理入口权限"],"table":[["资产","发现","状态"],["web-01","调试页面公开","待修复"],["db-01","无新增问题","已核验"]]}],"sheets":[{"name":"资产统计","rows":[[{"value":"项目"},{"value":"数量"}],[{"value":"已核查"},{"value":2}],[{"value":"待修复"},{"value":1}],[{"value":"合计"},{"formula":"SUM(B2:B3)","cached_value":3}],[{"value":"=不是公式"},{"value":false}]]},{"name":"风险清单","rows":[[{"value":"资产"},{"value":"状态"}],[{"value":"web-01"},{"value":"待修复"}]]}],"slides":[{"title":"月度安全报告","bullets":["范围：两台测试资产","结论：一项待修复风险"],"notes":"仅供测试的演示备注，不能当作生产安全结论。"},{"title":"下一步计划","bullets":["修复调试页面暴露","复验并归档证据"],"notes":"确认变更范围和维护窗口。"}]}`

func TestDocumentCreateFourFormatsRoundTripAndNoOverwrite(t *testing.T) {
	root := t.TempDir()
	if requested := os.Getenv("DEEPSENTRY_DOC_QA_DIR"); requested != "" {
		root = requested
		if err := os.MkdirAll(root, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, format := range []string{"docx", "xlsx", "pptx", "pdf"} {
		t.Run(format, func(t *testing.T) {
			name := filepath.Join(root, "sample."+format)
			out, err := DocumentCreate(name, format, reportSpec)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, "sha256") || !strings.Contains(out, "回读") {
				t.Fatal(out)
			}
			data, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := parseDocumentBytes(name, data, 20000, 100, 8)
			if err != nil {
				t.Fatal(err)
			}
			want := "月度安全报告"
			if format == "xlsx" {
				want = "资产统计"
			}
			if !strings.Contains(parsed.Text, want) {
				t.Fatalf("%s lost Unicode text: %s", format, parsed.Text)
			}
			if format == "pptx" && !strings.Contains(parsed.Text, "演示备注") {
				t.Fatal("speaker notes missing")
			}
			if format == "docx" && (len(parsed.Tables) != 1 || !strings.Contains(parsed.Tables[0], "web-01")) {
				t.Fatal("Word table structure missing")
			}
			if _, err := DocumentCreate(name, format, reportSpec); err == nil {
				t.Fatal("overwrote existing user artifact")
			}
		})
	}
}

func TestDocumentWindowUnicodePagination(t *testing.T) {
	withLocalExecutor(t)
	file := filepath.Join(t.TempDir(), "large.docx")
	spec := `{"title":"测试文档","sections":[{"heading":"正文","text":"` + strings.Repeat("分页证据不能丢失。", 300) + `"}]}`
	if _, err := DocumentCreate(file, "docx", spec); err != nil {
		t.Fatal(err)
	}
	first, err := DocumentParseWindow(Runtime{}, file, "text", 1000, 100, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "next_offset=1000") {
		t.Fatal(first)
	}
	second, err := DocumentParseWindow(Runtime{}, file, "text", 1000, 100, 8, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "分页证据") {
		t.Fatal(second)
	}
}

func TestPDFLayoutPreservesPageProvenance(t *testing.T) {
	text := pdfLayoutWithPages("第一页\f第二页\f")
	if !strings.Contains(text, "[PDF page 1]\n第一页") || !strings.Contains(text, "[PDF page 2]\n第二页") || strings.Contains(text, "page 3") {
		t.Fatal(text)
	}
}
