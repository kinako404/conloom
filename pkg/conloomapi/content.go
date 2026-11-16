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

// ValidateContent rejects a minor persona that is also adult or has an
// intimacy side, a bound alone included (see PersonaIntimacy.Empty),
// reporting both when both apply.
func ValidateContent(doc PersonaDoc) error {
	var errs []error
	if doc.Minor && doc.Adult {
		errs = append(errs, errors.New("a minor persona cannot be adult"))
	}
	if doc.Minor && !doc.Intimacy.Empty() {
		errs = append(errs, errors.New("a minor persona cannot have an intimacy side"))
	}
	return errors.Join(errs...)
}
