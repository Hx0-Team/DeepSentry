package analyzer

import (
	"fmt"
	"strings"
)

const summaryToolOutputMaxChars = 2000

// Like pi's compaction serializer: summarize visible text/calls, not replayed
// reasoning or image binaries. Tool responses are bounded; real user inputs
// remain complete and are split later if necessary. Work on a copy only.
func summaryHistoryProjection(history []Message) []Message {
	projected := make([]Message, 0, len(history))
	for _, message := range history {
		content := message.Content
		if isPrunableToolOutput(message) {
			content = summaryOutputPreview(content)
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[%s %s]\n%s", message.Role, message.Name, content)
		for _, call := range message.ToolCalls {
			fmt.Fprintf(&b, "\n[Tool call %s]: %s(%s)", call.ID, call.Function.Name, call.Function.Arguments)
		}
		for _, image := range message.Attachments {
			fmt.Fprintf(&b, "\n[Image evidence]: %s", image.Path)
		}
		projected = append(projected, Message{Role: "user", Content: b.String(), Synthetic: true})
	}
	return projected
}

func summaryOutputPreview(content string) string {
	runes := []rune(content)
	if len(runes) <= summaryToolOutputMaxChars {
		return content
	}
	const tailChars = 500
	return string(runes[:summaryToolOutputMaxChars-tailChars]) +
		fmt.Sprintf("\n[... %d chars omitted for summary ...]\n", len(runes)-summaryToolOutputMaxChars) +
		string(runes[len(runes)-tailChars:])
}
