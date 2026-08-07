package assistant

import "strings"

// buildUserContent turns a user message plus its uploaded attachments into the
// content blocks sent to the model, and the text shown in the transcript. Images
// (Data set) become image blocks the model can see; every attachment, image or
// not, is also listed by its in-app path in the text so the model knows the files
// exist and where. Returns the blocks and the display text (they carry the same note).
func buildUserContent(userText string, attachments []Attachment) (content []ContentBlock, displayText string) {
	var paths []string
	for _, a := range attachments {
		paths = append(paths, a.Path)
		if a.Data != "" && strings.HasPrefix(a.MediaType, "image/") {
			content = append(content, ContentBlock{
				Type:   "image",
				Source: &ImageSource{Type: "base64", MediaType: a.MediaType, Data: a.Data},
			})
		}
	}
	displayText = userText
	if len(paths) > 0 {
		note := "Attached files (saved in the app): " + strings.Join(paths, ", ")
		if displayText != "" {
			displayText += "\n\n" + note
		} else {
			displayText = note
		}
	}
	content = append(content, ContentBlock{Type: "text", Text: displayText})
	return content, displayText
}
