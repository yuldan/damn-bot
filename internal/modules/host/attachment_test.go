package host

import (
	"errors"
	"strings"
	"testing"
)

func pngMagic() []byte { return []byte("\x89PNG\r\n\x1a\nfixture") }

func TestAttachmentValidation(t *testing.T) {
	t.Parallel()
	ok := Attachment{Kind: AttachmentImage, Name: "cat.png", MIME: "image/png", Data: pngMagic()}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid image = %v", err)
	}
	cases := []struct {
		name string
		a    Attachment
	}{
		{"bad-kind", Attachment{Kind: "video", Name: "x", MIME: "image/png", Data: pngMagic()}},
		{"mime-mismatch", Attachment{Kind: AttachmentImage, Name: "x", MIME: "image/jpeg", Data: pngMagic()}},
		{"not-image", Attachment{Kind: AttachmentImage, Name: "x", MIME: "text/plain", Data: []byte("hi")}},
		{"empty-name", Attachment{Kind: AttachmentImage, Name: "", MIME: "image/png", Data: pngMagic()}},
		{"control-name", Attachment{Kind: AttachmentImage, Name: "a\nb", MIME: "image/png", Data: pngMagic()}},
		{"empty-data", Attachment{Kind: AttachmentAudio, Name: "a", MIME: "audio/wav"}},
		{"audio-mime", Attachment{Kind: AttachmentAudio, Name: "a", MIME: "image/png", Data: []byte("x")}},
		{"doc-mime", Attachment{Kind: AttachmentDocument, Name: "d", MIME: "audio/wav", Data: []byte("x")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.a.Validate(); !errors.Is(err, ErrAttachment) {
				t.Fatalf("err = %v", err)
			}
		})
	}
	if err := (Attachment{
		Kind: AttachmentImage, Name: "big", MIME: "image/png",
		Data: append(pngMagic(), make([]byte, int(MaxAttachmentBytes(AttachmentImage)))...),
	}).Validate(); !errors.Is(err, ErrAttachment) {
		t.Fatalf("oversize = %v", err)
	}
}

func TestAttachmentCountAndResult(t *testing.T) {
	t.Parallel()
	if err := validateAttachments(make([]Attachment, MaxAttachments+1)); !errors.Is(err, ErrAttachment) {
		t.Fatalf("count = %v", err)
	}
	r := Result{Content: "ok", Attachments: []Attachment{{Kind: AttachmentImage, Name: "cat", MIME: "image/png", Data: pngMagic()}}}
	if err := r.Validate(); err != nil {
		t.Fatalf("result = %v", err)
	}
	r.Attachments[0].MIME = "image/jpeg"
	if err := r.Validate(); !errors.Is(err, ErrAttachment) {
		t.Fatalf("result mime = %v", err)
	}
	if strings.Contains(r.Content, "base64") {
		t.Fatal("content unexpectedly carries base64")
	}
}
