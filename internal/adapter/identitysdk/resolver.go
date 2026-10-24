package identitysdk

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/domainry/domainry-foundation/httpresponse"
	identity "github.com/domainry/domainry-identity-sdk"
	identityevaluator "github.com/domainry/domainry-identity-sdk/authorization/evaluator"
	identitymiddleware "github.com/domainry/domainry-identity-sdk/httpmiddleware"
	imapplication "github.com/domainry/domainry-im/internal/application/im"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

type serviceActorContextKey struct{}

// ApplicationServiceAuthenticator verifies a short-lived, exact-grant
// Identity application-service token and publishes only the resulting IM
// service actor. It never turns that machine credential into an end-user
// Principal or an ordinary chat membership.
type ApplicationServiceAuthenticator struct {
	verifier    identity.ApplicationServiceTokenVerifier
	workspaceID identity.WorkspaceID
	audience    identity.ApplicationKey
}

func NewApplicationServiceAuthenticator(binding identity.Binding, workspaceID, audience string) (*ApplicationServiceAuthenticator, error) {
	capability, ok := binding.(identity.ApplicationServiceVerificationBinding)
	workspace, application := identity.WorkspaceID(strings.TrimSpace(workspaceID)), identity.ApplicationKey(strings.TrimSpace(audience))
	if !ok || capability.ApplicationServiceVerifier() == nil || !workspace.Valid() || !application.Valid() {
		return nil, errors.New("IM application-service verifier, Workspace and audience are required")
	}
	return &ApplicationServiceAuthenticator{verifier: capability.ApplicationServiceVerifier(), workspaceID: workspace, audience: application}, nil
}

func (a *ApplicationServiceAuthenticator) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := identitymiddleware.BearerToken(r)
		if !ok {
			httpresponse.WriteError(w, r, httpresponse.Failure{Status: http.StatusUnauthorized, Code: "auth.token_required"})
			return
		}
		grant := identity.ApplicationServiceGrant{Resource: "im.contexts.messages", Action: "send"}
		principal, err := a.verifier.Verify(r.Context(), identity.VerifyApplicationServiceTokenRequest{AccessToken: token, Audience: a.audience, Grant: grant})
		if err != nil {
			status := http.StatusUnauthorized
			var identityError *identity.Error
			if errors.As(err, &identityError) && (identityError.Class == identity.ErrorClassUnavailable || identityError.Class == identity.ErrorClassInternal) {
				status = http.StatusServiceUnavailable
			}
			httpresponse.WriteError(w, r, httpresponse.Failure{Status: status, Code: "im.application_service_token_invalid", Cause: err})
			return
		}
		if principal.Application.WorkspaceID != a.workspaceID || principal.Audience != a.audience || !principal.SubjectID.Valid() {
			httpresponse.WriteError(w, r, httpresponse.Failure{Status: http.StatusForbidden, Code: "im.application_service_scope_mismatch"})
			return
		}
		actor := model.Actor{WorkspaceID: string(a.workspaceID), SubjectID: string(principal.SubjectID), DisplayName: string(principal.Application.ApplicationKey)}
		ctx := context.WithValue(r.Context(), serviceActorContextKey{}, actor)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Resolver is the only boundary that translates Domainry Identity into the IM
// domain's actor. Business services never read authentication context.
type Resolver struct {
	workspaceID string
}

func NewResolver(workspaceID string) (*Resolver, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if !identity.WorkspaceID(workspaceID).Valid() {
		return nil, &model.Error{Class: "internal", Code: "im.identity_configuration_invalid"}
	}
	return &Resolver{workspaceID: workspaceID}, nil
}

func (r *Resolver) Resolve(ctx context.Context, actionKey string) (model.Actor, error) {
	if actor, ok := ctx.Value(serviceActorContextKey{}).(model.Actor); ok {
		if strings.TrimSpace(actionKey) != imapplication.ActionContextMessagesSend {
			return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
		}
		return actor, nil
	}
	requestIdentity, ok := identity.RequestIdentityFromContext(ctx)
	if !ok || !requestIdentity.Principal.Known {
		return model.Actor{}, &model.Error{Class: "unauthorized", Code: "auth.token_required"}
	}
	principal := requestIdentity.Principal
	workspaceID, subjectID := strings.TrimSpace(principal.WorkspaceID), strings.TrimSpace(principal.UserID)
	if workspaceID != r.workspaceID || !identity.SubjectID(subjectID).Valid() {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.workspace_scope_mismatch"}
	}
	if principal.User.UserID != "" && strings.TrimSpace(principal.User.UserID) != subjectID {
		return model.Actor{}, &model.Error{Class: "unavailable", Code: "im.identity_projection_mismatch"}
	}
	if principal.User.Status != "" && principal.User.Status != identity.UserStatusActive {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.account_inactive"}
	}
	actionKey = strings.TrimSpace(actionKey)
	if principal.AccessBundle == nil || !principal.HasPermission(actionKey) {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
	}
	if bundleWorkspace := strings.TrimSpace(string(principal.AccessBundle.Subject.WorkspaceID)); bundleWorkspace != "" && bundleWorkspace != workspaceID {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
	}
	if bundleSubject := strings.TrimSpace(string(principal.AccessBundle.Subject.SubjectID)); bundleSubject != "" && bundleSubject != subjectID {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
	}
	separator := strings.LastIndexByte(actionKey, '.')
	if separator <= 0 || separator == len(actionKey)-1 {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
	}
	resource, action := identity.ResourceType(actionKey[:separator]), identity.Action(actionKey[separator+1:])
	matchingPolicies := 0
	for _, policy := range principal.AccessBundle.DataPolicies {
		if policy.Resource != resource || policy.Action != action {
			continue
		}
		matchingPolicies++
		if policy.Effect != identity.EffectAllow || len(policy.DataScopes) != 1 || policy.DataScopes[0] != identity.DataScopeAll {
			return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied"}
		}
	}
	filter, err := identityevaluator.CompileRecordFilter(*principal.AccessBundle, resource, action, time.Now().UTC())
	if matchingPolicies != 1 || err != nil || !filter.Unrestricted || len(filter.Deny) != 0 {
		return model.Actor{}, &model.Error{Class: "forbidden", Code: "im.permission_denied", Cause: err}
	}
	return model.Actor{WorkspaceID: workspaceID, SubjectID: subjectID, DisplayName: firstNonBlank(principal.User.Name, principal.User.NativeName, subjectID)}, nil
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return "Domainry User"
}
