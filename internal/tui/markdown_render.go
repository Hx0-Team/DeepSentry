package tui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/ui"

	"github.com/charmbracelet/lipgloss"
)

var (
	mdH1Style          = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdH2Style          = lipgloss.NewStyle().Foreground(colorYellow).Bold(true)
	mdH3Style          = lipgloss.NewStyle().Foreground(colorGreen).Bold(true)
	mdBoldStyle        = lipgloss.NewStyle().Bold(true).Foreground(colorText)
	mdCodeStyle        = lipgloss.NewStyle().Foreground(colorYellow).Background(colorSurface)
	mdCodeBodyStyle    = lipgloss.NewStyle().Foreground(colorText)
	mdCodeCommentStyle = lipgloss.NewStyle().Foreground(colorMuted)
	mdMutedStyle       = lipgloss.NewStyle().Foreground(colorMuted)
	mdTableBorder      = lipgloss.NewStyle().Foreground(colorBorder)
	mdTableHeader      = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdListMarker       = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdNumberedList     = regexp.MustCompile(`^\s*(\d+)[.)]\s+(.+)$`)
)

func renderMarkdownReport(markdown string, width int) string {
	if width <= 0 {
		width = 80
	}
	innerW := width - styleAnswer.GetHorizontalFrameSize()
	innerW = max(1, innerW)
	rendered := renderMarkdownBlocks(markdown, innerW)
	return styleAnswer.Width(styleRenderWidth(styleAnswer, width)).Render(fitRenderedBlock(rendered, innerW))
}

func renderMarkdownAsk(markdown, timestamp string, width int) string {
	if width <= 0 {
		width = 80
	}
	innerW := width - styleToolBox.GetHorizontalFrameSize()
	innerW = max(1, innerW)
	header := mdH1Style.Render(sanitizeTUIText(timestamp) + "? 需要用户补充")
	body := renderMarkdownBlocks(markdown, innerW)
	if body == "" {
		body = mdMutedStyle.Render("请补充继续任务所需的信息。")
	}
	return styleToolBox.Width(styleRenderWidth(styleToolBox, width)).Render(fitRenderedBlock(header+"\n\n"+body, innerW))
}

func renderMarkdownConfirm(markdown, timestamp string, width int) string {
	if width <= 0 {
		width = 80
	}
	innerW := max(1, width-styleConfirmBox.GetHorizontalFrameSize())
	header := styleWarning.Render(sanitizeTUIText(timestamp) + "⚠ 需要确认")
	body := renderMarkdownBlocks(markdown, innerW)
	footer := mdBoldStyle.Render("Y 本次 · A 会话同类 · S 本次会话允许所有高危操作") + "\n" +
		mdBoldStyle.Render("N/Esc 拒绝 · Enter 拒绝")
	return styleConfirmBox.Width(styleRenderWidth(styleConfirmBox, width)).Render(fitRenderedBlock(header+"\n\n"+body+"\n\n"+footer, innerW))
}

func renderMarkdownBlocks(markdown string, width int) string {
	markdown = analyzer.UnescapeModelLineBreaks(markdown)
	markdown = sanitizeTUIText(markdown)
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")
	lines := strings.Split(strings.TrimSpace(markdown), "\n")
	out := make([]string, 0, len(lines))

	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			continue
		}

		if strings.HasPrefix(trimmed, "```") {
			language := strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			code := make([]string, 0)
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			out = append(out, renderCodeBlockLanguage(code, width, language))
			continue
		}

		if isMarkdownTableStart(lines, i) {
			tableLines := []string{lines[i], lines[i+1]}
			i += 2
			for i < len(lines) && isMarkdownTableLine(lines[i]) {
				tableLines = append(tableLines, lines[i])
				i++
			}
			i--
			out = append(out, renderMarkdownTable(tableLines, width)...)
			continue
		}

		if level, text, ok := parseMarkdownHeading(trimmed); ok {
			if len(out) > 0 && out[len(out)-1] != "" && level <= 2 {
				out = append(out, "")
			}
			out = append(out, renderMarkdownHeading(level, text, width))
			continue
		}

		if marker, text, ok := parseMarkdownList(trimmed); ok {
			out = append(out, renderMarkdownListItem(marker, text, width))
			continue
		}

		out = append(out, strings.Split(wrapDisplay(renderInlineMarkdown(trimmed), max(1, width)), "\n")...)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func parseMarkdownHeading(line string) (int, string, bool) {
	if !strings.HasPrefix(line, "#") {
		return 0, "", false
	}
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	return level, strings.TrimSpace(line[level:]), true
}

func renderMarkdownHeading(level int, text string, width int) string {
	text = normalizeMarkdownHeadingText(markdownInlinePlain(text))
	switch {
	case level <= 1:
		marker := "=="
		return wrapDisplay(mdH1Style.Render(marker+" "+text), max(1, width))
	case level == 2:
		marker := "--"
		return wrapDisplay(mdH2Style.Render(marker+" "+text), max(1, width))
	default:
		marker := ">"
		return wrapDisplay(mdH3Style.Render(marker+" "+text), max(1, width))
	}
}

func normalizeMarkdownHeadingText(text string) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return text
	}
	cluster, rest := firstDisplayCluster(text)
	if cluster == "" || rest == "" {
		return text
	}
	if !isMarkdownIconPrefix(cluster) {
		return text
	}
	if next, _ := firstDisplayCluster(rest); strings.TrimSpace(next) == "" {
		return text
	}
	return cluster + " " + rest
}

func isMarkdownIconPrefix(cluster string) bool {
	r, _ := utf8.DecodeRuneInString(cluster)
	if unicode.IsLetter(r) || unicode.IsDigit(r) {
		return false
	}
	if unicode.IsSymbol(r) {
		return true
	}
	return displayWidth(cluster) >= 2
}

func parseMarkdownList(line string) (string, string, bool) {
	if len(line) >= 2 {
		prefix := line[:2]
		if prefix == "- " || prefix == "* " || prefix == "+ " {
			return "•", strings.TrimSpace(line[2:]), true
		}
	}
	if m := mdNumberedList.FindStringSubmatch(line); len(m) == 3 {
		return m[1] + ".", strings.TrimSpace(m[2]), true
	}
	return "", "", false
}

func renderMarkdownListItem(marker, text string, width int) string {
	markerW := displayWidth(marker) + 1
	if width <= markerW {
		return wrapDisplay(renderInlineMarkdown(marker+" "+text), max(1, width))
	}
	bodyW := max(1, width-markerW)
	wrapped := strings.Split(wrapDisplay(renderInlineMarkdown(text), bodyW), "\n")
	for i, line := range wrapped {
		if i == 0 {
			wrapped[i] = mdListMarker.Render(marker) + " " + line
		} else {
			wrapped[i] = strings.Repeat(" ", markerW) + line
		}
	}
	return strings.Join(wrapped, "\n")
}

func renderInlineMarkdown(line string) string {
	var b strings.Builder
	for len(line) > 0 {
		codeStart := strings.Index(line, "`")
		boldStart := strings.Index(line, "**")
		switch {
		case codeStart >= 0 && (boldStart < 0 || codeStart < boldStart):
			b.WriteString(normalizeEmojiSpacing(line[:codeStart]))
			rest := line[codeStart+1:]
			if end := strings.Index(rest, "`"); end >= 0 {
				b.WriteString(mdCodeStyle.Render(rest[:end]))
				line = rest[end+1:]
			} else {
				b.WriteString("`" + rest)
				line = ""
			}
		case boldStart >= 0:
			b.WriteString(normalizeEmojiSpacing(line[:boldStart]))
			rest := line[boldStart+2:]
			if end := strings.Index(rest, "**"); end >= 0 {
				b.WriteString(mdBoldStyle.Render(normalizeEmojiSpacing(rest[:end])))
				line = rest[end+2:]
			} else {
				b.WriteString("**" + rest)
				line = ""
			}
		default:
			b.WriteString(normalizeEmojiSpacing(line))
			line = ""
		}
	}
	return b.String()
}

func markdownInlinePlain(line string) string {
	return stripANSI(renderInlineMarkdown(ui.TerminalText(line)))
}

func isMarkdownTableStart(lines []string, i int) bool {
	return i+1 < len(lines) && isMarkdownTableLine(lines[i]) && isMarkdownTableSeparator(lines[i+1])
}

func isMarkdownTableLine(line string) bool {
	line = strings.TrimSpace(line)
	return strings.Count(line, "|") >= 2
}

func isMarkdownTableSeparator(line string) bool {
	cells := splitMarkdownTableRow(line)
	if len(cells) == 0 {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(cell)
		if cell == "" {
			return false
		}
		cell = strings.Trim(cell, ":")
		if cell == "" || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(markdownInlinePlain(part)))
	}
	return cells
}

func renderMarkdownTable(lines []string, width int) []string {
	if len(lines) < 2 {
		return lines
	}
	rows := make([][]string, 0, len(lines)-1)
	for i, line := range lines {
		if i == 1 {
			continue
		}
		rows = append(rows, splitMarkdownTableRow(line))
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return []string{strings.Join(lines, "\n")}
	}
	cols := len(rows[0])
	for i := range rows {
		for len(rows[i]) < cols {
			rows[i] = append(rows[i], "")
		}
		if len(rows[i]) > cols {
			rows[i] = rows[i][:cols]
		}
	}
	tableW := max(1, width-2)
	// A technically valid grid with 1-2 cell columns is unreadable and drops
	// the values users need to make a decision. Prefer the stacked key/value
	// form until every column has a useful amount of space.
	if tableW < cols*7+1 {
		return renderMarkdownTableStacked(rows, width)
	}
	widths := markdownTableWidths(rows, tableW)
	if markdownTableWouldTruncate(rows, widths) {
		return renderMarkdownTableStacked(rows, width)
	}
	if tableTotalWidth(widths) > tableW {
		return renderMarkdownTableStacked(rows, width)
	}
	out := make([]string, 0, len(rows)+3)
	prefix := "  "
	out = append(out, prefix+tableBorderLine("top", widths))
	out = append(out, prefix+tableRow(rows[0], widths, true))
	out = append(out, prefix+tableBorderLine("mid", widths))
	for _, row := range rows[1:] {
		out = append(out, prefix+tableRow(row, widths, false))
	}
	out = append(out, prefix+tableBorderLine("bottom", widths))
	return out
}

func markdownTableWouldTruncate(rows [][]string, widths []int) bool {
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && displayWidth(cell) > widths[i] {
				return true
			}
		}
	}
	return false
}

func renderMarkdownTableStacked(rows [][]string, width int) []string {
	if len(rows) == 0 {
		return nil
	}
	headers := rows[0]
	data := rows[1:]
	if len(data) == 0 {
		data = rows[:1]
	}
	if isMarkdownLabelValueTable(headers) {
		return renderMarkdownLabelValueRows(data, width)
	}
	out := make([]string, 0, len(data))
	for _, row := range data {
		parts := make([]string, 0, len(headers))
		for i, header := range headers {
			value := ""
			if i < len(row) {
				value = row[i]
			}
			parts = append(parts, header+": "+value)
		}
		out = append(out, strings.Split(wrapDisplay(strings.Join(parts, " · "), max(1, width)), "\n")...)
	}
	return out
}

// isMarkdownLabelValueTable identifies the common two-column summary shape
// emitted by models, for example "项目 | 内容" or "字段 | 值". Repeating
// those generic headers for every stacked row produces mechanical text such
// as "项目: 任务成果 · 内容: ...". The first data cell is already the useful
// label, so render it as a definition-style list instead.
func isMarkdownLabelValueTable(headers []string) bool {
	if len(headers) != 2 {
		return false
	}
	left := strings.ToLower(strings.TrimSpace(headers[0]))
	right := strings.ToLower(strings.TrimSpace(headers[1]))
	leftLabels := map[string]bool{
		"项目": true, "字段": true, "名称": true, "属性": true, "类别": true, "类型": true,
		"item": true, "field": true, "name": true, "property": true, "category": true, "type": true,
	}
	rightLabels := map[string]bool{
		"内容": true, "值": true, "说明": true, "详情": true, "描述": true, "结果": true,
		"content": true, "value": true, "details": true, "description": true, "result": true,
	}
	return leftLabels[left] && rightLabels[right]
}

func renderMarkdownLabelValueRows(rows [][]string, width int) []string {
	width = max(1, width)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		label := ""
		value := ""
		if len(row) > 0 {
			label = strings.TrimSpace(row[0])
		}
		if len(row) > 1 {
			value = strings.TrimSpace(row[1])
		}
		if label == "" && value == "" {
			continue
		}
		prefix := "• "
		if label != "" {
			prefix += label + "："
		}
		if value == "" {
			out = append(out, mdListMarker.Render("•")+" "+mdTableHeader.Render(label))
			continue
		}
		bodyWidth := max(1, width-displayWidth(prefix))
		wrapped := strings.Split(wrapDisplay(value, bodyWidth), "\n")
		for i, line := range wrapped {
			if i == 0 {
				out = append(out, mdListMarker.Render("•")+" "+mdTableHeader.Render(label+"：")+line)
				continue
			}
			out = append(out, strings.Repeat(" ", displayWidth(prefix))+line)
		}
	}
	return out
}

func markdownTableWidths(rows [][]string, maxWidth int) []int {
	cols := len(rows[0])
	widths := make([]int, cols)
	for _, row := range rows {
		for i, cell := range row {
			w := displayWidth(cell)
			if w > widths[i] {
				widths[i] = w
			}
		}
	}
	for i := range widths {
		if widths[i] < 2 {
			widths[i] = 2
		}
		if widths[i] > 32 {
			widths[i] = 32
		}
	}
	for tableTotalWidth(widths) > maxWidth && len(widths) > 0 {
		idx := widestColumn(widths)
		if widths[idx] <= 2 {
			break
		}
		widths[idx]--
	}
	return widths
}

func tableTotalWidth(widths []int) int {
	total := 1
	for _, w := range widths {
		total += w + 3
	}
	return total
}

func widestColumn(widths []int) int {
	idx := 0
	for i, w := range widths {
		if w > widths[idx] {
			idx = i
		}
	}
	return idx
}

func tableBorderLine(kind string, widths []int) string {
	left, mid, right := "├", "┼", "┤"
	if ui.PlainTextMode() {
		left, mid, right = "+", "+", "+"
	} else {
		switch kind {
		case "top":
			left, mid, right = "╭", "┬", "╮"
		case "bottom":
			left, mid, right = "╰", "┴", "╯"
		}
	}
	if ui.PlainTextMode() {
		parts := make([]string, 0, len(widths))
		for _, w := range widths {
			parts = append(parts, strings.Repeat("-", w+2))
		}
		return mdTableBorder.Render(left + strings.Join(parts, mid) + right)
	}
	parts := make([]string, 0, len(widths))
	for _, w := range widths {
		parts = append(parts, strings.Repeat("─", w+2))
	}
	return mdTableBorder.Render(left + strings.Join(parts, mid) + right)
}

func tableRow(cells []string, widths []int, header bool) string {
	var b strings.Builder
	pipe := mdTableBorder.Render("|")
	if !ui.PlainTextMode() {
		pipe = mdTableBorder.Render("│")
	}
	b.WriteString(pipe)
	for i, w := range widths {
		cell := ""
		if i < len(cells) {
			cell = fitPlainLine(cells[i], w)
		}
		content := " " + cell + " "
		if header {
			content = mdTableHeader.Render(content)
		}
		b.WriteString(content)
		b.WriteString(pipe)
	}
	return b.String()
}

func fitPlainLine(s string, width int) string {
	s = ui.TerminalText(s)
	if displayWidth(s) > width {
		s = truncateDisplay(s, width, "…")
	}
	if gap := width - displayWidth(s); gap > 0 {
		s += strings.Repeat(" ", gap)
	}
	return s
}
