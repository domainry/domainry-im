package saas

import (
	"net/http/httptest"
	"testing"

	"github.com/domainry/domainry-foundation/apperror"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

type classificationCapture struct {
	*httptest.ResponseRecorder
	kind, code string
}

func (capture *classificationCapture) RecordError(kind, code string) {
	capture.kind, capture.code = kind, code
}

func TestIMErrorClassificationMatrix(t *testing.T) {
	tests := []struct {
		class apperror.ErrorKind
		code  string
		kind  string
	}{
		{apperror.KindUnavailable, "im.database_unavailable", "unavailable"},
		{apperror.KindConflict, "im.revision_fence_rejected", "conflict"},
		{apperror.KindBadRequest, "im.message_invalid", "bad_request"},
		{apperror.KindForbidden, "im.permission_denied", "forbidden"},
	}
	for _, test := range tests {
		capture := &classificationCapture{ResponseRecorder: httptest.NewRecorder()}
		writeError(capture, httptest.NewRequest("GET", "/", nil), &model.Error{Class: test.class, Code: test.code})
		if capture.kind != test.kind || capture.code != test.code {
			t.Fatalf("class=%q kind=%q code=%q", test.class, capture.kind, capture.code)
		}
	}
}
