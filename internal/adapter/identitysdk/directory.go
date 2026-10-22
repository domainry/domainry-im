package identitysdk

import (
	"context"
	"fmt"
	"strings"

	identity "github.com/domainry/domainry-identity-sdk"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

type Directory struct {
	projection identity.Projection
}

func NewDirectory(binding identity.Binding) (*Directory, error) {
	if binding == nil || nilCapability(binding.Projection()) {
		return nil, fmt.Errorf("IM Identity user projection is required")
	}
	return &Directory{projection: binding.Projection()}, nil
}

// ValidateMembers rejects unknown or inactive Identity users before their IDs
// become durable conversation membership. The application binding already
// scopes Projection to the configured workspace.
func (d *Directory) ValidateMembers(ctx context.Context, subjectIDs []string) error {
	if d == nil || d.projection == nil {
		return &model.Error{Class: "unavailable", Code: "im.member_directory_unavailable"}
	}
	for _, subjectID := range subjectIDs {
		subjectID = strings.TrimSpace(subjectID)
		user, found, err := d.projection.FindUser(ctx, identity.UserLookup{UserID: identity.SubjectID(subjectID)})
		if err != nil {
			return &model.Error{Class: "unavailable", Code: "im.member_directory_unavailable", Cause: err}
		}
		if !found || strings.TrimSpace(user.UserID) != subjectID || user.Status != identity.UserStatusActive {
			return model.Invalid("im.member_invalid")
		}
	}
	return nil
}
