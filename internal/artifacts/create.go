// Package artifacts produces portable Office/PDF documents from a bounded
// declarative specification. It never evaluates code or fetches remote assets.
package artifacts

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

type Section struct {
	Heading string     `json:"heading"`
	Text    string     `json:"text"`
	Bullets []string   `json:"bullets"`
	Table   [][]string `json:"table"`
}
type Cell struct {
	Value       any      `json:"value"`
	Formula     string   `json:"formula,omitempty"`
	CachedValue *float64 `json:"cached_value,omitempty"`
}
type Sheet struct {
	Name string   `json:"name"`
	Rows [][]Cell `json:"rows"`
}
type Slide struct {
	Title   string   `json:"title"`
	Bullets []string `json:"bullets"`
	Notes   string   `json:"notes"`
}
type Spec struct {
	Title    string    `json:"title"`
	Subtitle string    `json:"subtitle"`
	Author   string    `json:"author"`
	Sections []Section `json:"sections"`
	Sheets   []Sheet   `json:"sheets"`
	Slides   []Slide   `json:"slides"`
}

func ParseSpec(raw string) (Spec, error) {
	if len(raw) > 1<<20 {
		return Spec{}, fmt.Errorf("spec 最大 1 MiB；使用精简正文/表格而非整份日志")
	}
	var s Spec
	d := json.NewDecoder(strings.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&s); err != nil {
		return s, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return s, fmt.Errorf("spec 必须是一个 JSON 对象")
	}
	if strings.TrimSpace(s.Title) == "" {
		return s, fmt.Errorf("spec.title 不能为空")
	}
	if len(s.Sections) > 200 || len(s.Sheets) > 50 || len(s.Slides) > 100 {
		return s, fmt.Errorf("最多200章节、50工作表、100幻灯片")
	}
	for _, sheet := range s.Sheets {
		if len(sheet.Rows) > 10_000 {
			return s, fmt.Errorf("每工作表最多10000行")
		}
		for _, row := range sheet.Rows {
			if len(row) > 200 {
				return s, fmt.Errorf("每行最多200列")
			}
		}
	}
	return s, nil
}

func Create(format string, s Spec) ([]byte, []string, error) {
	var parts map[string]string
	var warnings []string
	switch format {
	case "docx":
		parts = wordParts(s)
	case "xlsx":
		if len(s.Sheets) == 0 {
			return nil, nil, fmt.Errorf("Excel 需要 spec.sheets（name/rows；每个单元格为 value/formula/cached_value 对象）")
		}
		seen := map[string]bool{}
		for _, sheet := range s.Sheets {
			if sheet.Name == "" || len([]rune(sheet.Name)) > 31 || strings.ContainsAny(sheet.Name, `[]:*?/\`) {
				return nil, nil, fmt.Errorf("工作表名称必须为1..31字符，不能包含 []:*?/\\")
			}
			name := strings.ToLower(sheet.Name)
			if seen[name] {
				return nil, nil, fmt.Errorf("工作表名重复: %s", sheet.Name)
			}
			seen[name] = true
			for _, row := range sheet.Rows {
				for _, cell := range row {
					if cell.Formula != "" && cell.CachedValue == nil {
						warnings = append(warnings, "部分公式未提供 cached_value，需在 Excel/LibreOffice 打开后重算；未声称已计算结果")
						break
					}
				}
			}
		}
		parts = excelParts(s)
	case "pptx":
		if len(s.Slides) == 0 {
			return nil, nil, fmt.Errorf("PPT 需要 spec.slides（title/bullets/notes）")
		}
		for _, slide := range s.Slides {
			if len([]rune(slide.Title)) > 40 || strings.ContainsAny(slide.Title, "\r\n") {
				return nil, nil, fmt.Errorf("幻灯片标题最多40字符且单段，请精简长标题")
			}
			if len(slide.Bullets) > 8 {
				return nil, nil, fmt.Errorf("每页最多8条要点，请拆页防溢出")
			}
			for _, bullet := range slide.Bullets {
				if len([]rune(bullet)) > 100 {
					return nil, nil, fmt.Errorf("每条要点最多100字符，请精简或拆页")
				}
			}
			// Count wrapped lines plus the inter-bullet spacing. Individual
			// length limits alone allow eight 100-character bullets to overflow.
			chars := 0
			for _, b := range slide.Bullets {
				chars += len([]rune(b)) + 2
			}
			columns, capacity := 40, 12
			if chars > 300 {
				columns, capacity = 50, 15
			}
			lines := max(0, len(slide.Bullets)-1)
			for _, b := range slide.Bullets {
				for _, p := range strings.Split(b, "\n") {
					lines += max(1, (len([]rune(p))+2+columns-1)/columns)
				}
			}
			if lines > capacity {
				return nil, nil, fmt.Errorf("幻灯片正文预计溢出，请拆页或把详情移入 notes")
			}
		}
		parts = powerPointParts(s)
	case "pdf":
		return reportPDF(s), []string{"中文PDF使用标准STSong CJK字体与Unicode映射，未嵌入字体；跨系统交付需用支持CJK的PDF阅读器复核"}, nil
	default:
		return nil, nil, fmt.Errorf("format 只能是 docx/xlsx/pptx/pdf")
	}
	parts["docProps/core.xml"] = xmlHeader + `<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>` + esc(s.Title) + `</dc:title><dc:creator>` + esc(s.Author) + `</dc:creator></cp:coreProperties>`
	var buffer bytes.Buffer
	zw := zip.NewWriter(&buffer)
	names := make([]string, 0, len(parts))
	for n := range parts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, e := zw.Create(n)
		if e != nil {
			return nil, nil, e
		}
		if _, e = io.WriteString(w, parts[n]); e != nil {
			return nil, nil, e
		}
	}
	if e := zw.Close(); e != nil {
		return nil, nil, e
	}
	return buffer.Bytes(), warnings, nil
}

const xmlHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`
const relNS = `http://schemas.openxmlformats.org/package/2006/relationships`
const officeRel = `http://schemas.openxmlformats.org/officeDocument/2006/relationships/`

func esc(s string) string { var b bytes.Buffer; _ = xml.EscapeText(&b, []byte(s)); return b.String() }
func rootRels(target string) string {
	return xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="rId1" Type="` + officeRel + `officeDocument" Target="` + target + `"/><Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/></Relationships>`
}
func contentTypes(overrides string) string {
	return xmlHeader + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>` + overrides + `</Types>`
}
func override(name, typ string) string {
	return `<Override PartName="/` + name + `" ContentType="application/vnd.openxmlformats-officedocument.` + typ + `+xml"/>`
}
