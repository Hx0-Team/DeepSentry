package artifacts

import (
	"fmt"
	"strings"
)

func wordParagraph(text, style string) string {
	return `<w:p><w:pPr><w:pStyle w:val="` + style + `"/></w:pPr><w:r><w:t xml:space="preserve">` + esc(text) + `</w:t></w:r></w:p>`
}
func wordParts(s Spec) map[string]string {
	var body strings.Builder
	body.WriteString(wordParagraph(s.Title, "Title"))
	if s.Subtitle != "" {
		body.WriteString(wordParagraph(s.Subtitle, "Subtitle"))
	}
	for _, section := range s.Sections {
		if section.Heading != "" {
			body.WriteString(wordParagraph(section.Heading, "Heading1"))
		}
		for _, paragraph := range strings.Split(section.Text, "\n") {
			if paragraph != "" {
				body.WriteString(wordParagraph(paragraph, "Normal"))
			}
		}
		for _, bullet := range section.Bullets {
			body.WriteString(wordParagraph("• "+bullet, "Normal"))
		}
		if len(section.Table) > 0 {
			body.WriteString(`<w:tbl><w:tblPr><w:tblW w:w="0" w:type="auto"/><w:tblBorders><w:top w:val="single" w:sz="4" w:color="C7D2E0"/><w:left w:val="single" w:sz="4" w:color="C7D2E0"/><w:bottom w:val="single" w:sz="4" w:color="C7D2E0"/><w:right w:val="single" w:sz="4" w:color="C7D2E0"/><w:insideH w:val="single" w:sz="4" w:color="C7D2E0"/><w:insideV w:val="single" w:sz="4" w:color="C7D2E0"/></w:tblBorders></w:tblPr>`)
			columns := 0
			for _, row := range section.Table {
				columns = max(columns, len(row))
			}
			body.WriteString(`<w:tblGrid>`)
			for i := 0; i < columns; i++ {
				body.WriteString(`<w:gridCol w:w="` + fmt.Sprint(9638/max(1, columns)) + `"/>`)
			}
			body.WriteString(`</w:tblGrid>`)
			for index, row := range section.Table {
				for len(row) < columns {
					row = append(row, "")
				}
				body.WriteString(`<w:tr>`)
				if index == 0 {
					body.WriteString(`<w:trPr><w:tblHeader/></w:trPr>`)
				}
				for _, cell := range row {
					body.WriteString(`<w:tc><w:tcPr><w:tcW w:w="0" w:type="auto"/>`)
					if index == 0 {
						body.WriteString(`<w:shd w:fill="E8EFF8"/>`)
					}
					body.WriteString(`</w:tcPr>` + wordParagraph(cell, "Normal") + `</w:tc>`)
				}
				body.WriteString(`</w:tr>`)
			}
			body.WriteString(`</w:tbl><w:p/>`)
		}
	}
	body.WriteString(`<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1134" w:right="1134" w:bottom="1134" w:left="1134"/></w:sectPr>`)
	return map[string]string{
		"_rels/.rels":                  rootRels("word/document.xml"),
		"[Content_Types].xml":          contentTypes(override("word/document.xml", "wordprocessingml.document.main") + override("word/styles.xml", "wordprocessingml.styles")),
		"word/document.xml":            xmlHeader + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `</w:body></w:document>`,
		"word/_rels/document.xml.rels": xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="rId1" Type="` + officeRel + `styles" Target="styles.xml"/></Relationships>`,
		"word/styles.xml":              xmlHeader + `<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Microsoft YaHei"/><w:sz w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="140" w:line="300" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults><w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style><w:style w:type="paragraph" w:styleId="Title"><w:name w:val="Title"/><w:rPr><w:b/><w:color w:val="17365D"/><w:sz w:val="40"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Subtitle"><w:name w:val="Subtitle"/><w:rPr><w:color w:val="607080"/><w:sz w:val="24"/></w:rPr></w:style><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:keepNext/><w:outlineLvl w:val="0"/><w:spacing w:before="240" w:after="140"/></w:pPr><w:rPr><w:b/><w:color w:val="17365D"/><w:sz w:val="28"/></w:rPr></w:style></w:styles>`,
	}
}
