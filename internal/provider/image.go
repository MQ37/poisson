package provider

import (
	"encoding/base64"
	"os"
)

// imageBlockBase64 reads an image ContentBlock's file and returns its base64
// data and media type. ok is false when the block isn't an image or the file
// can't be read (e.g. the /tmp file vanished after a reboot) — callers skip it
// rather than fail the whole request.
func imageBlockBase64(cb ContentBlock) (data, mediaType string, ok bool) {
	if cb.Type != "image" || cb.ImagePath == "" {
		return "", "", false
	}
	raw, err := os.ReadFile(cb.ImagePath)
	if err != nil {
		return "", "", false
	}
	mt := cb.MediaType
	if mt == "" {
		mt = "image/png"
	}
	return base64.StdEncoding.EncodeToString(raw), mt, true
}

// imageBlockDataURL returns the block as an OpenAI-style data URL
// ("data:image/png;base64,...."). ok is false when unavailable.
func imageBlockDataURL(cb ContentBlock) (string, bool) {
	data, mt, ok := imageBlockBase64(cb)
	if !ok {
		return "", false
	}
	return "data:" + mt + ";base64," + data, true
}

// oaiContentPart is one element of an OpenAI-compatible multimodal content
// array (used by the Ollama and xAI providers).
type oaiContentPart struct {
	Type     string       `json:"type"` // "text" | "image_url"
	Text     string       `json:"text,omitempty"`
	ImageURL *oaiImageURL `json:"image_url,omitempty"`
}

type oaiImageURL struct {
	URL string `json:"url"`
}

// openAIUserContent builds the "content" value for an OpenAI-compatible user
// message: a plain string when there are no usable images, otherwise a parts
// array with the text first followed by one image_url part per image.
func openAIUserContent(text string, images []ContentBlock) any {
	var parts []oaiContentPart
	for _, cb := range images {
		if url, ok := imageBlockDataURL(cb); ok {
			parts = append(parts, oaiContentPart{Type: "image_url", ImageURL: &oaiImageURL{URL: url}})
		}
	}
	if len(parts) == 0 {
		return text
	}
	if text != "" {
		parts = append([]oaiContentPart{{Type: "text", Text: text}}, parts...)
	}
	return parts
}
