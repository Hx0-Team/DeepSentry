package artifacts

import (
	"fmt"
	"strconv"
	"strings"
)

func columnName(index int) string {
	var s string
	for index > 0 {
		index--
		s = string(rune('A'+index%26)) + s
		index /= 26
	}
	return s
}
func excelParts(s Spec) map[string]string {
	parts := map[string]string{"_rels/.rels": rootRels("xl/workbook.xml")}
	var sheets, rels, types strings.Builder
	types.WriteString(override("xl/workbook.xml", "spreadsheetml.sheet.main"))
	types.WriteString(override("xl/styles.xml", "spreadsheetml.styles"))
	rels.WriteString(xmlHeader + `<Relationships xmlns="` + relNS + `">`)
	for i, sheet := range s.Sheets {
		id := strconv.Itoa(i + 1)
		fmt.Fprintf(&sheets, `<sheet name="%s" sheetId="%s" r:id="rId%s"/>`, esc(sheet.Name), id, id)
		rels.WriteString(`<Relationship Id="rId` + id + `" Type="` + officeRel + `worksheet" Target="worksheets/sheet` + id + `.xml"/>`)
		name := "xl/worksheets/sheet" + id + ".xml"
		types.WriteString(override(name, "spreadsheetml.worksheet"))
		var b strings.Builder
		b.WriteString(xmlHeader + `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><sheetViews><sheetView workbookViewId="0"><pane ySplit="1" topLeftCell="A2" activePane="bottomLeft" state="frozen"/></sheetView></sheetViews><sheetFormatPr defaultColWidth="22"/><sheetData>`)
		for r, row := range sheet.Rows {
			fmt.Fprintf(&b, `<row r="%d">`, r+1)
			for c, cell := range row {
				ref := columnName(c+1) + strconv.Itoa(r+1)
				style := 0
				if r == 0 {
					style = 1
				}
				if cell.Formula != "" {
					fmt.Fprintf(&b, `<c r="%s" s="%d"><f>%s</f>`, ref, style, esc(strings.TrimPrefix(cell.Formula, "=")))
					if cell.CachedValue != nil {
						fmt.Fprintf(&b, `<v>%s</v>`, strconv.FormatFloat(*cell.CachedValue, 'g', -1, 64))
					}
					b.WriteString(`</c>`)
					continue
				}
				switch v := cell.Value.(type) {
				case float64:
					fmt.Fprintf(&b, `<c r="%s" s="%d"><v>%s</v></c>`, ref, style, strconv.FormatFloat(v, 'g', -1, 64))
				case bool:
					n := 0
					if v {
						n = 1
					}
					fmt.Fprintf(&b, `<c r="%s" t="b" s="%d"><v>%d</v></c>`, ref, style, n)
				case nil:
					b.WriteString(`<c r="` + ref + `"/>`)
				default:
					fmt.Fprintf(&b, `<c r="%s" t="inlineStr" s="%d"><is><t xml:space="preserve">%s</t></is></c>`, ref, style, esc(fmt.Sprint(v)))
				}
			}
			b.WriteString(`</row>`)
		}
		b.WriteString(`</sheetData></worksheet>`)
		parts[name] = b.String()
	}
	rels.WriteString(`<Relationship Id="styles" Type="` + officeRel + `styles" Target="styles.xml"/></Relationships>`)
	parts["xl/_rels/workbook.xml.rels"] = rels.String()
	parts["xl/workbook.xml"] = xmlHeader + `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="` + strings.TrimSuffix(officeRel, "/") + `"><sheets>` + sheets.String() + `</sheets><calcPr calcId="191029" fullCalcOnLoad="1" forceFullCalc="1"/></workbook>`
	parts["[Content_Types].xml"] = contentTypes(types.String())
	parts["xl/styles.xml"] = xmlHeader + `<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><color rgb="FF17365D"/><sz val="11"/><name val="Calibri"/></font></fonts><fills count="3"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill><fill><patternFill patternType="solid"><fgColor rgb="FFE8EFF8"/><bgColor indexed="64"/></patternFill></fill></fills><borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders><cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs><cellXfs count="2"><xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"><alignment vertical="top" wrapText="1"/></xf><xf numFmtId="0" fontId="1" fillId="2" borderId="0" xfId="0" applyFill="1" applyFont="1"><alignment vertical="top" wrapText="1"/></xf></cellXfs><cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles></styleSheet>`
	return parts
}
