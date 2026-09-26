package identitysdk

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	identity "github.com/domainry/domainry-identity-sdk"
	imapplication "github.com/domainry/domainry-im/internal/application/im"
)

type Options struct {
	Factory     identity.Factory
	Application identity.ApplicationRef
}

// Open establishes the standalone Identity binding and reconciles IM's
// source-owned application and exact Permission catalog before serving traffic.
func Open(ctx context.Context, options Options) (identity.Binding, error) {
	if ctx == nil || options.Factory == nil {
		return nil, fmt.Errorf("IM SaaS Identity factory and lifecycle context are required")
	}
	application := options.Application
	if !application.WorkspaceID.Valid() || !application.ApplicationKey.Valid() {
		return nil, fmt.Errorf("IM SaaS Identity workspace and application are required")
	}
	binding, err := options.Factory.Open(ctx, application)
	if err != nil {
		return nil, fmt.Errorf("open IM SaaS Identity binding: %w", err)
	}
	if binding == nil {
		return nil, fmt.Errorf("IM SaaS Identity factory returned no binding")
	}
	closeRejected := func(err error) (identity.Binding, error) {
		_ = binding.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	descriptor := binding.Descriptor()
	if descriptor.Mode != identity.DeploymentModeSaaS {
		return closeRejected(fmt.Errorf("IM SaaS requires Identity SaaS, got %q", descriptor.Mode))
	}
	if descriptor.ProtocolVersion != identity.CurrentProtocolVersion || descriptor.BundleVersion != identity.CurrentPolicyBundleVersion || descriptor.AuthorizationVersion != identity.CurrentAuthorizationContractVersion {
		return closeRejected(fmt.Errorf("IM SaaS Identity protocol is incompatible"))
	}
	if strings.TrimSpace(descriptor.Issuer) == "" || strings.TrimSpace(descriptor.Audience) != string(application.ApplicationKey) {
		return closeRejected(fmt.Errorf("IM SaaS Identity issuer/audience scope is invalid"))
	}
	if nilCapability(binding.Tokens()) || nilCapability(binding.Authorization()) || nilCapability(binding.Principals()) || nilCapability(binding.Projection()) || nilCapability(binding.Applications()) || nilCapability(binding.Permissions()) {
		return closeRejected(fmt.Errorf("IM SaaS Identity capabilities are incomplete"))
	}
	if _, err := binding.Applications().Register(ctx, identity.ApplicationRegistration{Application: application}); err != nil {
		return closeRejected(fmt.Errorf("register IM Identity application: %w", err))
	}
	definitions, err := PermissionDefinitions()
	if err != nil {
		return closeRejected(fmt.Errorf("build IM Identity permissions: %w", err))
	}
	reader, ok := binding.Permissions().(identity.PermissionSnapshotReader)
	if !ok {
		return closeRejected(fmt.Errorf("IM SaaS Identity permission snapshot reader is unavailable"))
	}
	snapshotRequest := identity.PermissionSourceSnapshotRequest{Application: application, SourceOwner: imapplication.AuthorizationOwner}
	snapshot, err := reader.CurrentSourceSnapshot(ctx, snapshotRequest)
	if err != nil {
		return closeRejected(fmt.Errorf("read IM Identity permission snapshot: %w", err))
	}
	if err := snapshot.ValidateFor(snapshotRequest); err != nil {
		return closeRejected(fmt.Errorf("validate IM Identity permission snapshot: %w", err))
	}
	request, err := identity.NewPermissionReconcileRequest(application, imapplication.AuthorizationOwner, snapshot.SnapshotHash, definitions)
	if err != nil {
		return closeRejected(fmt.Errorf("build IM Identity permission snapshot: %w", err))
	}
	receipt, err := binding.Permissions().Reconcile(ctx, request)
	if err != nil {
		return closeRejected(fmt.Errorf("reconcile IM Identity permissions: %w", err))
	}
	if err := receipt.ValidateFor(request); err != nil {
		return closeRejected(fmt.Errorf("validate IM Identity permission receipt: %w", err))
	}
	return binding, nil
}

func PermissionDefinitions() ([]identity.PermissionDefinition, error) {
	actions, err := imapplication.AuthorizationActions()
	if err != nil {
		return nil, err
	}
	definitions := make([]identity.PermissionDefinition, 0, len(actions))
	for _, action := range actions {
		if action.Permission == nil {
			continue
		}
		permission := action.Permission
		definitions = append(definitions, identity.PermissionDefinition{PermissionKey: permission.Key, ResourceKey: permission.ResourceKey, OperationKey: permission.OperationKey, Label: permission.Label, Description: permission.Description, Category: permission.Category, SourceKind: action.SourceKind})
	}
	return definitions, nil
}

func nilCapability(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
