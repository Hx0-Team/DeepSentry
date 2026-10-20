package artifacts

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf16"
)

func pdfUnicode(text string) string {
	// The standard GB1 font substitutes missing Latin middle-dot/bullet
	// glyphs inconsistently across viewers. Use a portable list separator.
	text = strings.NewReplacer("·", "-", "•", "-").Replace(text)
	units := utf16.Encode([]rune(text))
	raw := []byte{}
	for _, u := range units {
		raw = binary.BigEndian.AppendUint16(raw, u)
	}
	return "<" + strings.ToUpper(hex.EncodeToString(raw)) + ">"
}

// A4 flowing report with standard CJK font and an identity ToUnicode map.
// The specification is plain data; no PDF actions, JavaScript or external links.
func reportPDF(s Spec) []byte {
	type line struct {
		text  string
		size  int
		color string
	}
	var lines []line
	add := func(text string, size int, color string) {
		for _, p := range strings.Split(text, "\n") {
			r := []rune(p)
			width := 80
			if size >= 18 {
				width = 36
			} else {
				width = 46
			}
			if len(r) == 0 {
				lines = append(lines, line{"", size, color})
			}
			for len(r) > 0 {
				n := min(width, len(r))
				lines = append(lines, line{string(r[:n]), size, color})
				r = r[n:]
			}
		}
	}
	add(s.Title, 20, "0.09 0.21 0.36")
	if s.Subtitle != "" {
		add(s.Subtitle, 11, "0.38 0.44 0.50")
	}
	for _, section := range s.Sections {
		add("", 11, "0 0 0")
		if section.Heading != "" {
			add(section.Heading, 15, "0.09 0.21 0.36")
		}
		add(section.Text, 11, "0.15 0.18 0.22")
		for _, b := range section.Bullets {
			add("· "+b, 11, "0.15 0.18 0.22")
		}
		for _, row := range section.Table {
			add(strings.Join(row, "  |  "), 10, "0.15 0.18 0.22")
		}
	}
	var pages []string
	var content strings.Builder
	y := 780.0
	flush := func() { pages = append(pages, content.String()); content.Reset(); y = 780 }
	for _, l := range lines {
		if y-float64(l.size+10) < 60 {
			flush()
		}
		fmt.Fprintf(&content, "BT /F1 %d Tf %s rg 1 0 0 1 50 %.1f Tm %s Tj ET\n", l.size, l.color, y, pdfUnicode(l.text))
		y -= float64(l.size + 10)
	}
	if content.Len() > 0 || len(pages) == 0 {
		flush()
	}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", `<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] /ToUnicode 5 0 R >>`, `<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /FontDescriptor 6 0 R /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 4 >> /DW 1000 /W [1 95 500] >>`}
	cmap := "/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n/CMapName /UnicodeIdentity def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n1 beginbfrange\n<0000> <FFFF> <0000>\nendbfrange\nendcmap\nCMapName currentdict /CMap defineresource pop\nend\nend"
	objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(cmap), cmap))
	objects = append(objects, `<< /Type /FontDescriptor /FontName /STSong-Light /Flags 6 /FontBBox [0 -200 1000 900] /ItalicAngle 0 /Ascent 880 /Descent -120 /CapHeight 880 /StemV 80 >>`)
	var kids []string
	for i, page := range pages {
		pageID := len(objects) + 1
		streamID := pageID + 1
		kids = append(kids, fmt.Sprintf("%d 0 R", pageID))
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595.28 841.89] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", streamID))
		page += fmt.Sprintf("BT /F1 9 Tf 0.4 0.4 0.4 rg 1 0 0 1 280 32 Tm %s Tj ET\n", pdfUnicode(fmt.Sprintf("%d / %d", i+1, len(pages))))
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(page), page))
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), strings.Join(kids, " "))
	var out bytes.Buffer
	out.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}
