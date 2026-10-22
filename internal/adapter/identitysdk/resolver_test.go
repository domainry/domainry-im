package identitysdk

import (
	"context"
	"errors"
	"testing"
	"time"

	identity "github.com/domainry/domainry-identity-sdk"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

func TestResolverRequiresExactPermissionAndCanonicalAllScope(t *testing.T) {
	resolver, err := NewResolver("workspace-a")
	if err != nil {
		t.Fatal(err)
	}
	bundle := identity.AccessBundle{
		ContractVersion:       identity.CurrentPolicyBundleVersion,
		AuthorizationRevision: "revision-1",
		ExpiresAt:             time.Now().Add(time.Hour),
		Subject:               identity.Subject{WorkspaceID: "workspace-a", SubjectID: "user-a"},
		FunctionGrants:        []identity.FunctionGrant{{Resource: "im.threads", Action: "list", Effect: identity.EffectAllow}},
		DataPolicies:          []identity.DataPolicy{{Key: "im.threads.list", Resource: "im.threads", Action: "list", Effect: identity.EffectAllow, DataScopes: []identity.DataScope{identity.DataScopeAll}}},
	}
	principal := identity.Principal{Known: true, WorkspaceID: "workspace-a", UserID: "user-a", User: identity.User{ID: 9876, UserID: "user-a", Name: "User A", Status: identity.UserStatusActive}, AccessBundle: &bundle}
	ctx := identity.WithRequestIdentity(context.Background(), identity.RequestIdentity{Principal: principal})
	actor, err := resolver.Resolve(ctx, "im.threads.list")
	if err != nil || actor.SubjectID != "user-a" || actor.DisplayName != "User A" {
		t.Fatalf("actor=%#v err=%v", actor, err)
	}
	principal.User.UserID = "another-user"
	mismatched := identity.WithRequestIdentity(context.Background(), identity.RequestIdentity{Principal: principal})
	if _, err := resolver.Resolve(mismatched, "im.threads.list"); errorClass(err) != "unavailable" {
		t.Fatalf("business identity mismatch was not rejected: %v", err)
	}
	if _, err := resolver.Resolve(ctx, "im.threads.create"); errorClass(err) != "forbidden" {
		t.Fatalf("missing permission error=%v", err)
	}
	bundle.DataPolicies[0].DataScopes = []identity.DataScope{identity.DataScopeOwner}
	if _, err := resolver.Resolve(ctx, "im.threads.list"); errorClass(err) != "forbidden" {
		t.Fatalf("restricted scope error=%v", err)
	}
}

func TestPermissionDefinitionsAreExactActionProjection(t *testing.T) {
	definitions, err := PermissionDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions) != 16 {
		t.Fatalf("definitions=%d want=16", len(definitions))
	}
	for _, definition := range definitions {
		if definition.PermissionKey != definition.ResourceKey+"."+definition.OperationKey || definition.SourceKind != "module_http" {
			t.Fatalf("permission is not an exact Action projection: %#v", definition)
		}
	}
}

func TestDirectoryRejectsUnknownAndInactiveMembers(t *testing.T) {
	directory := &Directory{projection: projectionStub{users: map[string]identity.User{
		"active":     {ID: 1, UserID: "active", Status: identity.UserStatusActive},
		"inactive":   {ID: 2, UserID: "inactive", Status: "disabled"},
		"mismatched": {ID: 3, UserID: "active", Status: identity.UserStatusActive},
	}}}
	if err := directory.ValidateMembers(context.Background(), []string{"active"}); err != nil {
		t.Fatal(err)
	}
	for _, subjectID := range []string{"missing", "inactive", "mismatched"} {
		if err := directory.ValidateMembers(context.Background(), []string{subjectID}); errorClass(err) != "bad_request" {
			t.Fatalf("subject=%s err=%v", subjectID, err)
		}
	}
}

type projectionStub struct{ users map[string]identity.User }

func (p projectionStub) FindUser(_ context.Context, lookup identity.UserLookup) (identity.User, bool, error) {
	user, found := p.users[string(lookup.UserID)]
	return user, found, nil
}
func (projectionStub) FindOrganizationUnit(context.Context, identity.OrganizationUnitLookup) (identity.OrganizationUnit, bool, error) {
	return identity.OrganizationUnit{}, false, nil
}
func (projectionStub) ListUsers(context.Context, identity.ProjectionQuery) ([]identity.User, error) {
	return nil, nil
}
func (projectionStub) ListRoles(context.Context, identity.ProjectionQuery) ([]identity.Role, error) {
	return nil, nil
}
func (projectionStub) ListUserRoleAssignments(context.Context, identity.UserRoleAssignmentQuery) ([]identity.UserRoleAssignment, error) {
	return nil, nil
}

func errorClass(err error) string {
	var value *model.Error
	if errors.As(err, &value) {
		return string(value.Class)
	}
	return ""
}
