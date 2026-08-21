package assistant

import "strings"

// attachmentNotePrefix marks the text block that tells the model where uploaded
// files were saved. It is meant for the model, not the user, so the transcript
// display skips any block starting with it (see toItems).
const (
	attachmentNotePrefix = "Attached files (saved in the app): "
)

// buildUserContent turns a user message plus its uploaded attachments into the
// content blocks sent to the model, and the text shown in the transcript. Images
// (Data set) become image blocks the model can see; every attachment's in-app path
// goes into a separate note block so the model knows the files exist and where --
// that note is not shown to the user. displayText is the user's own text only.
func buildUserContent(userText string, attachments []Attachment) (content []ContentBlock, displayText string) {
	var paths []string
	for _, a := range attachments {
		paths = append(paths, a.Path)
		if a.Data != "" && strings.HasPrefix(a.MediaType, "image/") {
			content = append(content, ContentBlock{
				Type:   blockImage,
				Source: &ImageSource{Type: "base64", MediaType: a.MediaType, Data: a.Data},
			})
		}
	}
	if userText != "" {
		content = append(content, ContentBlock{Type: blockText, Text: userText})
	}
	if len(paths) > 0 {
		content = append(content, ContentBlock{Type: blockText, Text: attachmentNotePrefix + strings.Join(paths, ", ")})
	}
	if len(content) == 0 {
		content = append(content, ContentBlock{Type: blockText, Text: userText}) // keep a valid (empty) message
	}
	return content, userText
}
