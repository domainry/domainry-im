package im_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	imstore "github.com/domainry/domainry-im/internal/infrastructure/persistence/database/im"
	"github.com/domainry/domainry-im/internal/infrastructure/persistence/migrationhost"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	ormpostgres "github.com/domainry/domainry-orm/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresTwoAccountConversation(t *testing.T) {
	dsn := os.Getenv("DOMAINRY_IM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DOMAINRY_IM_TEST_POSTGRES_DSN is not set")
	}

	dialectValue, err := ormdialect.New(ormdialect.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("domainry_im_test_%d", time.Now().UTC().UnixNano())
	renderer := dialectValue.WithSchema(schema)
	connectionConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if connectionConfig.RuntimeParams == nil {
		connectionConfig.RuntimeParams = make(map[string]string)
	}
	connectionConfig.RuntimeParams["search_path"] = dialectValue.Identifier(schema)
	database := stdlib.OpenDB(*connectionConfig)
	if err := database.PingContext(t.Context()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if _, err := database.ExecContext(t.Context(), "CREATE SCHEMA "+dialectValue.Identifier(schema)); err != nil {
		database.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = database.ExecContext(cleanupContext, "DROP SCHEMA IF EXISTS "+dialectValue.Identifier(schema)+" CASCADE")
		_ = database.Close()
	})

	registrar := &migrationhost.Registrar{DB: database, Renderer: renderer}
	if err := registrar.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	migrations, err := imstore.SchemaMigrations(renderer)
	if err != nil {
		t.Fatal(err)
	}
	if err := registrar.ApplyOwnedMigrations(t.Context(), imstore.MigrationOwner, migrations); err != nil {
		t.Fatal(err)
	}
	store, err := imstore.NewStore(database, renderer, ormpostgres.NewProfile())
	if err != nil {
		t.Fatal(err)
	}

	alice := model.Actor{WorkspaceID: "workspace-postgres", SubjectID: "alice", DisplayName: "Alice"}
	bob := model.Actor{WorkspaceID: "workspace-postgres", SubjectID: "bob", DisplayName: "Bob"}
	now := time.Now().UTC()
	thread, err := store.CreateThread(t.Context(), alice, model.Thread{
		ID:        "thread-postgres",
		Kind:      model.ThreadDirect,
		MemberIDs: []string{alice.SubjectID, bob.SubjectID},
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	message := model.Message{
		ID:              "message-postgres-1",
		ClientMessageID: "alice-offline-1",
		SenderID:        alice.SubjectID,
		SenderName:      alice.DisplayName,
		Kind:            model.MessageText,
		Text:            "hello bob",
		CreatedAt:       now.Add(time.Second),
	}
	created, inserted, err := store.AppendMessage(t.Context(), alice, thread.ID, message)
	if err != nil || !inserted || created.Sequence != 1 {
		t.Fatalf("append=%#v inserted=%v err=%v", created, inserted, err)
	}
	replayed, inserted, err := store.AppendMessage(t.Context(), alice, thread.ID, message)
	if err != nil || inserted || replayed.ID != created.ID || replayed.Sequence != created.Sequence {
		t.Fatalf("replay=%#v inserted=%v err=%v", replayed, inserted, err)
	}
	page, err := store.ListMessages(t.Context(), bob, thread.ID, 0, 20)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Text != message.Text {
		t.Fatalf("messages=%#v err=%v", page, err)
	}
	cursor, changed, err := store.MarkRead(t.Context(), bob, thread.ID, created.Sequence, now.Add(2*time.Second))
	if err != nil || !changed || cursor.Sequence != created.Sequence {
		t.Fatalf("cursor=%#v changed=%v err=%v", cursor, changed, err)
	}
}
