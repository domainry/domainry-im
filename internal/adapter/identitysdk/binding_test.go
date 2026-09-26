package identitysdk

import (
	"context"
	"testing"

	identity "github.com/domainry/domainry-identity-sdk"
	application "github.com/domainry/domainry-im/internal/application/im"
)

func TestOpenRegistersApplicationAndReconcilesEveryIMPermission(t *testing.T) {
	app := identity.ApplicationRef{WorkspaceID: "workspace-a", ApplicationKey: "domainry-im"}
	applications := &applicationRegistryTestStub{}
	permissions := &permissionRegistryTestStub{}
	binding := &bindingTestStub{
		descriptor: identity.Descriptor{
			ProtocolVersion: identity.CurrentProtocolVersion, BundleVersion: identity.CurrentPolicyBundleVersion,
			AuthorizationVersion: identity.CurrentAuthorizationContractVersion, Mode: identity.DeploymentModeSaaS,
			Issuer: "https://identity.example.test", Audience: string(app.ApplicationKey),
		},
		applications: applications,
		permissions:  permissions,
	}
	factory := &factoryTestStub{binding: binding}
	opened, err := Open(t.Context(), Options{Factory: factory, Application: app})
	if err != nil {
		t.Fatal(err)
	}
	if opened != binding || factory.opened != app || applications.registered.Application != app {
		t.Fatalf("application binding was not registered: opened=%v factory=%#v registration=%#v", opened, factory.opened, applications.registered)
	}
	if permissions.reconciled.Application != app || permissions.reconciled.SourceOwner != application.AuthorizationOwner || len(permissions.reconciled.Definitions) != 16 {
		t.Fatalf("permission reconcile=%#v", permissions.reconciled)
	}
	for _, definition := range permissions.reconciled.Definitions {
		if definition.PermissionKey != definition.ResourceKey+"."+definition.OperationKey {
			t.Fatalf("permission is not exact: %#v", definition)
		}
	}
}

func TestOpenFailsClosedForIncompatibleIdentityMode(t *testing.T) {
	app := identity.ApplicationRef{WorkspaceID: "workspace-a", ApplicationKey: "domainry-im"}
	binding := &bindingTestStub{
		descriptor: identity.Descriptor{
			ProtocolVersion: identity.CurrentProtocolVersion, BundleVersion: identity.CurrentPolicyBundleVersion,
			AuthorizationVersion: identity.CurrentAuthorizationContractVersion, Mode: identity.DeploymentModeModule,
			Issuer: "issuer", Audience: string(app.ApplicationKey),
		},
		applications: &applicationRegistryTestStub{}, permissions: &permissionRegistryTestStub{},
	}
	if _, err := Open(t.Context(), Options{Factory: &factoryTestStub{binding: binding}, Application: app}); err == nil || !binding.closed {
		t.Fatalf("incompatible binding was not rejected and closed: err=%v closed=%v", err, binding.closed)
	}
}

type factoryTestStub struct {
	binding identity.Binding
	opened  identity.ApplicationRef
}

func (s *factoryTestStub) Open(_ context.Context, application identity.ApplicationRef) (identity.Binding, error) {
	s.opened = application
	return s.binding, nil
}

type bindingTestStub struct {
	descriptor   identity.Descriptor
	applications *applicationRegistryTestStub
	permissions  *permissionRegistryTestStub
	closed       bool
}

func (s *bindingTestStub) Descriptor() identity.Descriptor            { return s.descriptor }
func (*bindingTestStub) Authentication() identity.Authentication      { return nil }
func (*bindingTestStub) Tokens() identity.TokenVerifier               { return tokenVerifierTestStub{} }
func (*bindingTestStub) Authorization() identity.Authorization        { return authorizationTestStub{} }
func (*bindingTestStub) Principals() identity.PrincipalResolver       { return principalResolverTestStub{} }
func (*bindingTestStub) Projection() identity.Projection              { return projectionStub{} }
func (s *bindingTestStub) Applications() identity.ApplicationRegistry { return s.applications }
func (s *bindingTestStub) Permissions() identity.PermissionRegistry   { return s.permissions }
func (*bindingTestStub) Credentials() identity.CredentialManager      { return nil }
func (*bindingTestStub) ApplicationServices() identity.ApplicationServiceAuthentication {
	return applicationServiceTestStub{}
}
func (s *bindingTestStub) Close(context.Context) error { s.closed = true; return nil }

type tokenVerifierTestStub struct{}

func (tokenVerifierTestStub) Verify(context.Context, identity.VerifyTokenRequest) (identity.VerifiedToken, error) {
	return identity.VerifiedToken{}, nil
}

type authorizationTestStub struct{}

func (authorizationTestStub) ResolveAccess(context.Context, identity.AccessBundleRequest) (identity.AccessBundle, error) {
	return identity.AccessBundle{}, nil
}
func (authorizationTestStub) Reauthorize(context.Context, identity.DecisionRequest) (identity.AccessDecision, error) {
	return identity.AccessDecision{}, nil
}

type principalResolverTestStub struct{}

func (principalResolverTestStub) Resolve(context.Context, identity.PrincipalResolutionRequest) (identity.PrincipalResolution, error) {
	return identity.PrincipalResolution{}, nil
}

type applicationServiceTestStub struct{}

func (applicationServiceTestStub) Exchange(context.Context, identity.ExchangeApplicationServiceTokenRequest) (identity.ApplicationServiceToken, error) {
	return identity.ApplicationServiceToken{}, nil
}
func (applicationServiceTestStub) Verify(context.Context, identity.VerifyApplicationServiceTokenRequest) (identity.ApplicationServicePrincipal, error) {
	return identity.ApplicationServicePrincipal{}, nil
}

type applicationRegistryTestStub struct {
	registered identity.ApplicationRegistration
}

func (s *applicationRegistryTestStub) Register(_ context.Context, registration identity.ApplicationRegistration) (identity.ApplicationRegistrationReceipt, error) {
	s.registered = registration
	return identity.ApplicationRegistrationReceipt{Application: registration.Application, RedirectURLs: registration.RedirectURLs, Status: "active"}, nil
}

type permissionRegistryTestStub struct {
	reconciled identity.PermissionReconcileRequest
}

func (*permissionRegistryTestStub) CurrentSourceSnapshot(_ context.Context, request identity.PermissionSourceSnapshotRequest) (identity.PermissionSourceSnapshot, error) {
	return identity.PermissionSourceSnapshot{WorkspaceID: request.Application.WorkspaceID, SourceOwner: request.SourceOwner}, nil
}

func (s *permissionRegistryTestStub) Reconcile(_ context.Context, request identity.PermissionReconcileRequest) (identity.PermissionReconcileReceipt, error) {
	s.reconciled = request
	return identity.PermissionReconcileReceipt{
		WorkspaceID: request.Application.WorkspaceID, SourceOwner: request.SourceOwner,
		PreviousSnapshotHash: request.PreviousSnapshotHash, SnapshotHash: request.SnapshotHash,
		DefinitionCount: len(request.Definitions), Inserted: len(request.Definitions),
	}, nil
}
