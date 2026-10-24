package saas

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/domainry/domainry-foundation/httpresponse"
	identitysdk "github.com/domainry/domainry-identity-sdk"
	identityprincipal "github.com/domainry/domainry-identity-sdk/authorization/principal"
	identitymiddleware "github.com/domainry/domainry-identity-sdk/httpmiddleware"
	identityremote "github.com/domainry/domainry-identity-sdk/remote"
	identityadapter "github.com/domainry/domainry-im/internal/adapter/identitysdk"
	knowledgeadapter "github.com/domainry/domainry-im/internal/adapter/knowledgefiles"
	conversationservice "github.com/domainry/domainry-im/internal/domain/conversation/service"
	"github.com/domainry/domainry-im/internal/infrastructure/config"
	imstore "github.com/domainry/domainry-im/internal/infrastructure/persistence/database/im"
	"github.com/domainry/domainry-im/internal/infrastructure/persistence/migrationhost"
	httptransport "github.com/domainry/domainry-im/internal/transport/http/saas"
	knowledgefiles "github.com/domainry/domainry-knowledge-sdk/files"
)

type Service struct {
	handler  http.Handler
	app      *conversationservice.Service
	identity identitysdk.Binding
}

func Open(ctx context.Context, settings config.Config) (*Service, error) {
	identityFactory := identityremote.NewFactory(identityremote.Config{Endpoint: settings.IdentityEndpoint, WorkspaceID: settings.IdentityWorkspaceID, Issuer: settings.IdentityIssuer, Audience: settings.IdentityApplicationKey, ServiceAccessToken: settings.IdentityServiceAccessToken})
	identityBinding, err := identityadapter.Open(ctx, identityadapter.Options{Factory: identityFactory, Application: identitysdk.ApplicationRef{WorkspaceID: identitysdk.WorkspaceID(settings.IdentityWorkspaceID), ApplicationKey: identitysdk.ApplicationKey(settings.IdentityApplicationKey)}})
	if err != nil {
		return nil, fmt.Errorf("open Domainry Identity binding: %w", err)
	}
	closeIdentity := true
	defer func() {
		if closeIdentity {
			_ = identityBinding.Close(context.WithoutCancel(ctx))
		}
	}()
	authenticator, err := identityprincipal.NewAuthenticator(identityBinding, identityprincipal.Options{})
	if err != nil {
		return nil, err
	}
	security, err := identitymiddleware.New(authenticator, identitymiddleware.WithAuthorization(identityBinding.Authorization()), identitymiddleware.WithBindingCredential(identityBinding))
	if err != nil {
		return nil, err
	}

	databaseResources, err := openDatabase(ctx, settings)
	if err != nil {
		return nil, err
	}
	database := databaseResources.database
	closeDatabase := true
	defer func() {
		if closeDatabase {
			_ = database.Close()
		}
	}()
	renderer := databaseResources.renderer
	registrar := &migrationhost.Registrar{DB: database, Renderer: renderer}
	if err := databaseResources.applyMigrations(ctx, func() error {
		if err := registrar.Prepare(ctx); err != nil {
			return err
		}
		migrations, err := imstore.SchemaMigrations(renderer)
		if err != nil {
			return err
		}
		return registrar.ApplyOwnedMigrations(ctx, imstore.MigrationOwner, migrations)
	}); err != nil {
		return nil, err
	}
	repository, err := imstore.NewStore(database, renderer, databaseResources.profile)
	if err != nil {
		return nil, err
	}
	fileService, err := knowledgefiles.OpenRemote(ctx, knowledgefiles.RemoteConfig{Endpoint: settings.KnowledgeEndpoint, ServiceAccessToken: settings.KnowledgeServiceAccessToken}, settings.KnowledgeRuntimeID)
	if err != nil {
		return nil, fmt.Errorf("open Domainry Knowledge file service: %w", err)
	}
	mediaStore, err := knowledgeadapter.New(fileService, settings.KnowledgeRuntimeID)
	if err != nil {
		return nil, err
	}
	directory, err := identityadapter.NewDirectory(identityBinding)
	if err != nil {
		return nil, err
	}
	application, err := conversationservice.New(repository, mediaStore, directory)
	if err != nil {
		return nil, err
	}
	actors, err := identityadapter.NewResolver(settings.IdentityWorkspaceID)
	if err != nil {
		return nil, err
	}
	api, err := httptransport.New(application, actors, settings.SSEPollInterval, settings.SSEHeartbeat)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /live", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		if err := application.Ready(r.Context()); err != nil {
			httpresponse.WriteError(w, r, httpresponse.Failure{Status: http.StatusServiceUnavailable, Code: "im.not_ready", Cause: err})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	serviceAuth, err := identityadapter.NewApplicationServiceAuthenticator(identityBinding, settings.IdentityWorkspaceID, settings.IdentityApplicationKey)
	if err != nil {
		return nil, err
	}
	// This exact signed route is more specific than /im/ and therefore cannot
	// fall through to the end-user bearer-token middleware.
	mux.Handle("POST /im/contexts/{namespace}/{resource_type}/{resource_id}/messages", serviceAuth.Authenticate(api))
	mux.Handle("/im/", security.Authenticate(security.RequirePasswordChanged(api)))
	closeDatabase, closeIdentity = false, false
	return &Service{handler: mux, app: application, identity: identityBinding}, nil
}

func (s *Service) Handler() http.Handler { return s.handler }

func (s *Service) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	var errs []error
	if s.app != nil {
		errs = append(errs, s.app.Close())
	}
	if s.identity != nil {
		errs = append(errs, s.identity.Close(ctx))
	}
	return errors.Join(errs...)
}

func ensureParent(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve IM database path: %w", err)
	}
	return ensureDirectory(filepath.Dir(absolute))
}
