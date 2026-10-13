package streamjson

import (
	"encoding/json"
	"testing"
)

// TestUserInputBlocks locks the content-block shape the claude CLI accepts on
// --input-format stream-json (verified behaviorally against claude 2.1.x): a
// leading text block followed by base64 image blocks in the Anthropic Messages
// form {type:"image",source:{type:"base64",media_type,data}}.
func TestUserInputBlocks(t *testing.T) {
	line, err := UserInputBlocks("hello", []ImageBlock{
		{MediaType: "image/png", Data: "AAAA"},
	}, []DocumentBlock{
		{MediaType: "application/pdf", Data: "BBBB"},
	})
	if err != nil {
		t.Fatalf("UserInputBlocks: %v", err)
	}

	var got struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content []struct {
				Type   string `json:"type"`
				Text   string `json:"text"`
				Source struct {
					Type      string `json:"type"`
					MediaType string `json:"media_type"`
					Data      string `json:"data"`
				} `json:"source"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Type != "user" || got.Message.Role != "user" {
		t.Fatalf("wrong envelope: %+v", got)
	}
	if len(got.Message.Content) != 3 {
		t.Fatalf("want 3 content blocks, got %d", len(got.Message.Content))
	}
	if c := got.Message.Content[0]; c.Type != "text" || c.Text != "hello" {
		t.Fatalf("block 0 not the text block: %+v", c)
	}
	img := got.Message.Content[1]
	if img.Type != "image" || img.Source.Type != "base64" ||
		img.Source.MediaType != "image/png" || img.Source.Data != "AAAA" {
		t.Fatalf("block 1 not the image block: %+v", img)
	}
	doc := got.Message.Content[2]
	if doc.Type != "document" || doc.Source.Type != "base64" ||
		doc.Source.MediaType != "application/pdf" || doc.Source.Data != "BBBB" {
		t.Fatalf("block 2 not the document block: %+v", doc)
	}
}

// TestUserInputBlocksTextOnly omits the text block when text is empty, so an
// image-only paste sends just the image.
func TestUserInputBlocksTextOnly(t *testing.T) {
	line, err := UserInputBlocks("", []ImageBlock{{MediaType: "image/png", Data: "AAAA"}}, nil)
	if err != nil {
		t.Fatalf("UserInputBlocks: %v", err)
	}
	var got struct {
		Message struct {
			Content []map[string]any `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Message.Content) != 1 {
		t.Fatalf("image-only: want 1 block, got %d", len(got.Message.Content))
	}
	if got.Message.Content[0]["type"] != "image" {
		t.Fatalf("image-only: want image block, got %v", got.Message.Content[0]["type"])
	}
}
