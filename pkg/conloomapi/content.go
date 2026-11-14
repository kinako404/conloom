package conloomapi

import "errors"

// ContentMode is how freely a persona may handle adult content in a
// conversation. Every case other than the one ModeFor names is
// ContentProtected.
type ContentMode string

const (
	ContentProtected ContentMode = "protected"
	ContentOpen      ContentMode = "open"
)

// ModeFor returns ContentOpen only for an adult persona that is not a minor,
// in a private conversation of type convType.
func ModeFor(doc PersonaDoc, convType string) ContentMode {
	if doc.Adult && !doc.Minor && convType == ConvPrivate {
		return ContentOpen
	}
	return ContentProtected
}

// ValidateContent rejects a persona marked both minor and adult.
func ValidateContent(doc PersonaDoc) error {
	if doc.Adult && doc.Minor {
		return errors.New("a minor persona cannot be adult")
	}
	return nil
}
