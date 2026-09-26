package model

import "errors"

var (
	ErrThreadNotFound     = errors.New("IM thread not found")
	ErrNotMember          = errors.New("IM thread membership required")
	ErrAttachmentNotFound = errors.New("IM attachment not found")
	ErrQuickReplyNotFound = errors.New("IM quick reply not found")
	ErrConflict           = errors.New("IM state conflict")
)

// Error is a transport-independent application error. HTTP mapping belongs to
// the transport package, so the domain does not import net/http.
type Error struct {
	Class string
	Code  string
	Cause error
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func Invalid(code string) error { return &Error{Class: "bad_request", Code: code} }

func RepositoryError(err error) error {
	switch {
	case errors.Is(err, ErrThreadNotFound), errors.Is(err, ErrNotMember):
		return &Error{Class: "not_found", Code: "im.thread_not_found", Cause: err}
	case errors.Is(err, ErrAttachmentNotFound):
		return &Error{Class: "not_found", Code: "im.attachment_not_found", Cause: err}
	case errors.Is(err, ErrQuickReplyNotFound):
		return &Error{Class: "not_found", Code: "im.quick_reply_not_found", Cause: err}
	case errors.Is(err, ErrConflict):
		return &Error{Class: "conflict", Code: "im.conflict", Cause: err}
	case err != nil:
		return &Error{Class: "internal", Code: "im.persistence_failed", Cause: err}
	default:
		return nil
	}
}
