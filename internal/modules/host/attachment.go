package host

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Attachment limits for one module tool result (GAP-REL-14).
const (
	MaxAttachments    = 8
	MaxAttachmentName = 256
	MaxAttachmentMIME = 128
	MaxAttachmentAll  = 64 << 20
)

// AttachmentKind is the closed attachment class.
type AttachmentKind string

// Attachment kinds.
const (
	AttachmentImage    AttachmentKind = "image"
	AttachmentDocument AttachmentKind = "document"
	AttachmentAudio    AttachmentKind = "audio"
)

// Attachment is one binary tool attachment. Data is bounded and never placed
// in the prompt as base64 by the core.
type Attachment struct {
	Kind AttachmentKind
	Name string
	MIME string
	Data []byte
}

// MaxAttachmentBytes returns the per-kind byte cap.
func MaxAttachmentBytes(k AttachmentKind) int64 {
	switch k {
	case AttachmentImage:
		return 32 << 20
	case AttachmentDocument:
		return 16 << 20
	case AttachmentAudio:
		return 25 << 20
	}
	return 0
}

// ErrAttachment rejects an invalid attachment.
var ErrAttachment = errors.New("host: invalid attachment")

// Validate checks kind, name, MIME, magic bytes and the byte cap.
func (a Attachment) Validate() error {
	if err := a.validateShape(); err != nil {
		return err
	}
	limit := MaxAttachmentBytes(a.Kind)
	if len(a.Data) == 0 || int64(len(a.Data)) > limit {
		return fmt.Errorf("%w: %s has %d bytes (max %d)", ErrAttachment, a.Name, len(a.Data), limit)
	}
	switch a.Kind {
	case AttachmentImage:
		sniff := http.DetectContentType(a.Data)
		if !strings.HasPrefix(a.MIME, "image/") || !strings.HasPrefix(sniff, "image/") {
			return fmt.Errorf("%w: %s is not an image (%q/%q)", ErrAttachment, a.Name, a.MIME, sniff)
		}
		if sniff != a.MIME {
			return fmt.Errorf("%w: %s MIME %q but magic is %q", ErrAttachment, a.Name, a.MIME, sniff)
		}
	case AttachmentDocument:
		if !strings.HasPrefix(a.MIME, "application/") && !strings.HasPrefix(a.MIME, "text/") {
			return fmt.Errorf("%w: %s document MIME %q", ErrAttachment, a.Name, a.MIME)
		}
	case AttachmentAudio:
		if !strings.HasPrefix(a.MIME, "audio/") {
			return fmt.Errorf("%w: %s audio MIME %q", ErrAttachment, a.Name, a.MIME)
		}
	}
	return nil
}

func (a Attachment) validateShape() error {
	if MaxAttachmentBytes(a.Kind) == 0 {
		return fmt.Errorf("%w: kind %q", ErrAttachment, a.Kind)
	}
	if a.Name == "" || utf8.RuneCountInString(a.Name) > MaxAttachmentName || strings.ContainsAny(a.Name, "\x00\r\n") {
		return fmt.Errorf("%w: name %q", ErrAttachment, a.Name)
	}
	if a.MIME == "" || len(a.MIME) > MaxAttachmentMIME || strings.ContainsAny(a.MIME, "\x00\r\n \t") {
		return fmt.Errorf("%w: MIME %q", ErrAttachment, a.MIME)
	}
	return nil
}

// validateAttachments enforces count and total size.
func validateAttachments(as []Attachment) error {
	if len(as) > MaxAttachments {
		return fmt.Errorf("%w: %d > %d", ErrAttachment, len(as), MaxAttachments)
	}
	var total int64
	for _, a := range as {
		if err := a.Validate(); err != nil {
			return err
		}
		total += int64(len(a.Data))
	}
	if total > MaxAttachmentAll {
		return fmt.Errorf("%w: total %d > %d", ErrAttachment, total, MaxAttachmentAll)
	}
	return nil
}
