package artifacts

import (
	"fmt"
	"strconv"
	"strings"
)

const drawingNS = "http://schemas.openxmlformats.org/drawingml/2006/main"
const presentationNS = "http://schemas.openxmlformats.org/presentationml/2006/main"

func slideText(id int, name, text string, x, y, w, h, size int, color string) string {
	var paragraphs strings.Builder
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&paragraphs, `<a:p><a:r><a:rPr lang="zh-CN" sz="%d"><a:solidFill><a:srgbClr val="%s"/></a:solidFill><a:latin typeface="Calibri"/><a:ea typeface="Microsoft YaHei"/></a:rPr><a:t>%s</a:t></a:r><a:endParaRPr lang="zh-CN" sz="%d"/></a:p>`, size, color, esc(line), size)
	}
	return fmt.Sprintf(`<p:sp><p:nvSpPr><p:cNvPr id="%d" name="%s"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="%d" y="%d"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom><a:noFill/></p:spPr><p:txBody><a:bodyPr wrap="square"/><a:lstStyle/>%s</p:txBody></p:sp>`, id, esc(name), x, y, w, h, paragraphs.String())
}

const groupTree = `<p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="0" cy="0"/><a:chOff x="0" y="0"/><a:chExt cx="0" cy="0"/></a:xfrm></p:grpSpPr>`

func powerPointParts(s Spec) map[string]string {
	parts := map[string]string{"_rels/.rels": rootRels("ppt/presentation.xml")}
	var ids, rels, types strings.Builder
	types.WriteString(override("ppt/presentation.xml", "presentationml.presentation.main"))
	rels.WriteString(xmlHeader + `<Relationships xmlns="` + relNS + `">`)
	for i, slide := range s.Slides {
		n := strconv.Itoa(i + 1)
		fmt.Fprintf(&ids, `<p:sldId id="%d" r:id="rId%s"/>`, 256+i, n)
		rels.WriteString(`<Relationship Id="rId` + n + `" Type="` + officeRel + `slide" Target="slides/slide` + n + `.xml"/>`)
		name := "ppt/slides/slide" + n + ".xml"
		types.WriteString(override(name, "presentationml.slide"))
		body := slideText(2, "Title", slide.Title, 540000, 360000, 11112000, 1000000, 3000, "17365D")
		var bullets []string
		for _, b := range slide.Bullets {
			bullets = append(bullets, "• "+b)
		}
		size := 2100
		chars := 0
		for _, b := range bullets {
			chars += len([]rune(b))
		}
		if chars > 300 {
			size = 1700
		}
		body += slideText(3, "Body", strings.Join(bullets, "\n\n"), 540000, 1580000, 11112000, 4400000, size, "27364A")
		body += slideText(4, "Page", n, 11200000, 6250000, 500000, 350000, 1100, "607080")
		parts[name] = xmlHeader + `<p:sld xmlns:a="` + drawingNS + `" xmlns:r="` + strings.TrimSuffix(officeRel, "/") + `" xmlns:p="` + presentationNS + `"><p:cSld><p:bg><p:bgPr><a:solidFill><a:srgbClr val="FFFFFF"/></a:solidFill><a:effectLst/></p:bgPr></p:bg><p:spTree>` + groupTree + body + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`
		slideRels := `<Relationship Id="layout" Type="` + officeRel + `slideLayout" Target="../slideLayouts/slideLayout1.xml"/>`
		if slide.Notes != "" {
			noteName := "ppt/notesSlides/notesSlide" + n + ".xml"
			types.WriteString(override(noteName, "presentationml.notesSlide"))
			slideRels += `<Relationship Id="notes" Type="` + officeRel + `notesSlide" Target="../notesSlides/notesSlide` + n + `.xml"/>`
			parts[noteName] = xmlHeader + `<p:notes xmlns:a="` + drawingNS + `" xmlns:p="` + presentationNS + `"><p:cSld><p:spTree>` + groupTree + `<p:sp><p:nvSpPr><p:cNvPr id="2" name="Notes"/><p:cNvSpPr/><p:nvPr><p:ph type="body" idx="1"/></p:nvPr></p:nvSpPr><p:spPr/><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:t>` + esc(slide.Notes) + `</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:notes>`
			parts["ppt/notesSlides/_rels/notesSlide"+n+".xml.rels"] = xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="slide" Type="` + officeRel + `slide" Target="../slides/slide` + n + `.xml"/><Relationship Id="master" Type="` + officeRel + `notesMaster" Target="../notesMasters/notesMaster1.xml"/></Relationships>`
		}
		parts["ppt/slides/_rels/slide"+n+".xml.rels"] = xmlHeader + `<Relationships xmlns="` + relNS + `">` + slideRels + `</Relationships>`
	}
	rels.WriteString(`<Relationship Id="master" Type="` + officeRel + `slideMaster" Target="slideMasters/slideMaster1.xml"/><Relationship Id="notesMaster" Type="` + officeRel + `notesMaster" Target="notesMasters/notesMaster1.xml"/></Relationships>`)
	parts["ppt/_rels/presentation.xml.rels"] = rels.String()
	parts["ppt/presentation.xml"] = xmlHeader + `<p:presentation xmlns:a="` + drawingNS + `" xmlns:r="` + strings.TrimSuffix(officeRel, "/") + `" xmlns:p="` + presentationNS + `"><p:sldMasterIdLst><p:sldMasterId id="2147483648" r:id="master"/></p:sldMasterIdLst><p:notesMasterIdLst><p:notesMasterId r:id="notesMaster"/></p:notesMasterIdLst><p:sldIdLst>` + ids.String() + `</p:sldIdLst><p:sldSz cx="12192000" cy="6858000" type="screen16x9"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`
	clrMap := `<p:clrMap accent1="accent1" accent2="accent2" accent3="accent3" accent4="accent4" accent5="accent5" accent6="accent6" bg1="lt1" bg2="lt2" folHlink="folHlink" hlink="hlink" tx1="dk1" tx2="dk2"/>`
	parts["ppt/slideMasters/slideMaster1.xml"] = xmlHeader + `<p:sldMaster xmlns:a="` + drawingNS + `" xmlns:r="` + strings.TrimSuffix(officeRel, "/") + `" xmlns:p="` + presentationNS + `"><p:cSld><p:spTree>` + groupTree + `</p:spTree></p:cSld>` + clrMap + `<p:sldLayoutIdLst><p:sldLayoutId id="2147483649" r:id="layout"/></p:sldLayoutIdLst><p:txStyles><p:titleStyle/><p:bodyStyle/><p:otherStyle/></p:txStyles></p:sldMaster>`
	parts["ppt/slideLayouts/slideLayout1.xml"] = xmlHeader + `<p:sldLayout xmlns:a="` + drawingNS + `" xmlns:p="` + presentationNS + `" type="blank" preserve="1"><p:cSld name="Blank"><p:spTree>` + groupTree + `</p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sldLayout>`
	parts["ppt/slideMasters/_rels/slideMaster1.xml.rels"] = xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="layout" Type="` + officeRel + `slideLayout" Target="../slideLayouts/slideLayout1.xml"/><Relationship Id="theme" Type="` + officeRel + `theme" Target="../theme/theme1.xml"/></Relationships>`
	parts["ppt/slideLayouts/_rels/slideLayout1.xml.rels"] = xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="master" Type="` + officeRel + `slideMaster" Target="../slideMasters/slideMaster1.xml"/></Relationships>`
	parts["ppt/notesMasters/notesMaster1.xml"] = xmlHeader + `<p:notesMaster xmlns:a="` + drawingNS + `" xmlns:p="` + presentationNS + `"><p:cSld><p:spTree>` + groupTree + `</p:spTree></p:cSld>` + clrMap + `<p:notesStyle/></p:notesMaster>`
	parts["ppt/notesMasters/_rels/notesMaster1.xml.rels"] = xmlHeader + `<Relationships xmlns="` + relNS + `"><Relationship Id="theme" Type="` + officeRel + `theme" Target="../theme/theme1.xml"/></Relationships>`
	parts["ppt/theme/theme1.xml"] = simpleTheme()
	for name, typ := range map[string]string{"ppt/slideMasters/slideMaster1.xml": "presentationml.slideMaster", "ppt/slideLayouts/slideLayout1.xml": "presentationml.slideLayout", "ppt/notesMasters/notesMaster1.xml": "presentationml.notesMaster", "ppt/theme/theme1.xml": "theme"} {
		types.WriteString(override(name, typ))
	}
	parts["[Content_Types].xml"] = contentTypes(types.String())
	return parts
}

func simpleTheme() string {
	var colors strings.Builder
	for _, pair := range [][2]string{{"dk1", "27364A"}, {"lt1", "FFFFFF"}, {"dk2", "17365D"}, {"lt2", "E8EFF8"}, {"accent1", "2F75B5"}, {"accent2", "00A8A8"}, {"accent3", "77933C"}, {"accent4", "8064A2"}, {"accent5", "F79646"}, {"accent6", "C0504D"}, {"hlink", "0563C1"}, {"folHlink", "954F72"}} {
		colors.WriteString(`<a:` + pair[0] + `><a:srgbClr val="` + pair[1] + `"/></a:` + pair[0] + `>`)
	}
	fill := `<a:solidFill><a:schemeClr val="phClr"/></a:solidFill>`
	return xmlHeader + `<a:theme xmlns:a="` + drawingNS + `" name="DeepSentry Business"><a:themeElements><a:clrScheme name="Business">` + colors.String() + `</a:clrScheme><a:fontScheme name="Office"><a:majorFont><a:latin typeface="Calibri"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface="Arial"/></a:majorFont><a:minorFont><a:latin typeface="Calibri"/><a:ea typeface="Microsoft YaHei"/><a:cs typeface="Arial"/></a:minorFont></a:fontScheme><a:fmtScheme name="Office"><a:fillStyleLst>` + strings.Repeat(fill, 3) + `</a:fillStyleLst><a:lnStyleLst>` + strings.Repeat(`<a:ln w="12700">`+fill+`<a:prstDash val="solid"/></a:ln>`, 3) + `</a:lnStyleLst><a:effectStyleLst>` + strings.Repeat(`<a:effectStyle><a:effectLst/></a:effectStyle>`, 3) + `</a:effectStyleLst><a:bgFillStyleLst>` + strings.Repeat(fill, 3) + `</a:bgFillStyleLst></a:fmtScheme></a:themeElements></a:theme>`
}
