package builtin

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Word and DrawingML use the same row/cell/text local names, so tables retain
// row/column structure in DOCX and PPTX instead of becoming unrelated strings.
func extractOfficeTables(raw []byte) []string {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var out []string
	var table, cell strings.Builder
	inTable, inCell, inText := false, false, false
	var row []string
	for {
		token, err := d.Token()
		if err != nil {
			break
		}
		switch v := token.(type) {
		case xml.StartElement:
			switch v.Name.Local {
			case "tbl":
				inTable = true
				table.Reset()
			case "tr":
				if inTable {
					row = nil
				}
			case "tc":
				if inTable {
					inCell = true
					cell.Reset()
				}
			case "t":
				inText = inCell
			}
		case xml.CharData:
			if inText {
				cell.Write(v)
			}
		case xml.EndElement:
			switch v.Name.Local {
			case "t":
				inText = false
			case "p":
				if inCell && cell.Len() > 0 {
					cell.WriteByte('\n')
				}
			case "tc":
				if inCell {
					row = append(row, strings.TrimSpace(cell.String()))
					inCell = false
				}
			case "tr":
				if inTable {
					table.WriteString("| " + strings.Join(row, " | ") + " |\n")
				}
			case "tbl":
				if inTable {
					out = append(out, fmt.Sprintf("Table %d:\n%s", len(out)+1, table.String()))
					inTable = false
				}
			}
		}
	}
	return out
}

func parsePPTXDocument(data []byte, maxText int) (documentParseResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return documentParseResult{}, err
	}
	files := zipFileMap(zr)
	names := sortedZipNames(files, "ppt/slides/slide", ".xml")
	// Numeric fallback, then presentation relationship order if provided.
	sort.Slice(names, func(i, j int) bool { return slideNumber(names[i]) < slideNumber(names[j]) })
	if ordered := presentationSlideOrder(files); len(ordered) > 0 {
		names = ordered
	}
	var text strings.Builder
	var tables, meta, warnings []string
	meta = append(meta, fmt.Sprintf("slides=%d", len(names)))
	for index, name := range names {
		raw, err := readZipEntryLimited(files[name], maxOOXMLEntryRead)
		if err != nil {
			return documentParseResult{}, err
		}
		fmt.Fprintf(&text, "Slide %d (%s):\n%s\n", index+1, name, extractDrawingText(raw))
		for _, table := range extractOfficeTables(raw) {
			tables = append(tables, fmt.Sprintf("Slide %d %s", index+1, table))
		}
		meta = append(meta, fmt.Sprintf("slide[%d].images=%d", index+1, bytes.Count(raw, []byte(":blip"))))
		// Notes are linked to the slide, never assumed to share its number.
		relsName := path.Join(path.Dir(name), "_rels", path.Base(name)+".rels")
		if rel := files[relsName]; rel != nil {
			relRaw, _ := readZipEntryLimited(rel, maxOOXMLEntryRead)
			for _, target := range officeRelationships(relRaw, "/notesSlide") {
				note := path.Clean(path.Join(path.Dir(name), target))
				if f := files[note]; f != nil {
					b, _ := readZipEntryLimited(f, maxOOXMLEntryRead)
					fmt.Fprintf(&text, "Speaker notes:\n%s\n", extractDrawingText(b))
				}
			}
		}
	}
	if len(names) == 0 {
		warnings = append(warnings, "没有可解析的幻灯片")
	}
	warnings = append(warnings, "图片/图表只报告结构与数量，不能声称已理解未做 OCR/视觉分析的图片")
	return documentParseResult{Format: "PowerPoint PPTX", Parser: "OOXML slides/tables/speaker notes", Meta: meta, Text: truncateDocumentText(text.String(), maxText), Tables: tables, Warnings: warnings}, nil
}

func slideNumber(name string) int {
	n, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(path.Base(name), "slide"), ".xml"))
	return n
}

func extractDrawingText(raw []byte) string {
	d := xml.NewDecoder(bytes.NewReader(raw))
	var b strings.Builder
	inText := false
	for {
		t, e := d.Token()
		if e == io.EOF || e != nil {
			break
		}
		switch v := t.(type) {
		case xml.StartElement:
			if v.Name.Local == "t" {
				inText = true
			}
		case xml.CharData:
			if inText {
				b.Write(v)
			}
		case xml.EndElement:
			if v.Name.Local == "t" {
				inText = false
			}
			if v.Name.Local == "p" {
				b.WriteByte('\n')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func officeRelationships(raw []byte, kind string) map[string]string {
	d := xml.NewDecoder(bytes.NewReader(raw))
	out := map[string]string{}
	for {
		t, e := d.Token()
		if e != nil {
			break
		}
		if v, ok := t.(xml.StartElement); ok && v.Name.Local == "Relationship" {
			attrs := map[string]string{}
			for _, a := range v.Attr {
				attrs[a.Name.Local] = a.Value
			}
			if strings.HasSuffix(attrs["Type"], kind) && attrs["TargetMode"] != "External" {
				out[attrs["Id"]] = attrs["Target"]
			}
		}
	}
	return out
}

func presentationSlideOrder(files map[string]*zip.File) []string {
	f, r := files["ppt/presentation.xml"], files["ppt/_rels/presentation.xml.rels"]
	if f == nil || r == nil {
		return nil
	}
	relRaw, _ := readZipEntryLimited(r, maxOOXMLEntryRead)
	rels := officeRelationships(relRaw, "/slide")
	raw, _ := readZipEntryLimited(f, maxOOXMLEntryRead)
	d := xml.NewDecoder(bytes.NewReader(raw))
	var out []string
	for {
		t, e := d.Token()
		if e != nil {
			break
		}
		if v, ok := t.(xml.StartElement); ok && v.Name.Local == "sldId" {
			for _, a := range v.Attr {
				if a.Name.Local == "id" {
					if target := rels[a.Value]; target != "" {
						name := path.Clean(path.Join("ppt", target))
						if strings.HasPrefix(target, "/") {
							name = strings.TrimPrefix(target, "/")
						}
						if files[name] != nil {
							out = append(out, name)
						}
					}
				}
			}
		}
	}
	return out
}
