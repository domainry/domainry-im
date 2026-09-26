package im_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/domainry/domainry-foundation/schemaownership"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	imstore "github.com/domainry/domainry-im/internal/infrastructure/persistence/database/im"
	"github.com/domainry/domainry-im/internal/infrastructure/persistence/migrationhost"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	ormsqlite "github.com/domainry/domainry-orm/sqlite"
	_ "modernc.org/sqlite"
)

func TestMessageCatchUpTagSearchReceiptsAndDurableSync(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	alice := model.Actor{WorkspaceID: "workspace-a", SubjectID: "alice", DisplayName: "Alice"}
	bob := model.Actor{WorkspaceID: "workspace-a", SubjectID: "bob", DisplayName: "Bob"}
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	thread, err := store.CreateThread(ctx, alice, model.Thread{ID: "thread-1", Kind: model.ThreadDirect, MemberIDs: []string{"alice", "bob"}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := store.SyncState(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	first := model.Message{ID: "message-1", ClientMessageID: "offline-1", SenderID: alice.SubjectID, SenderName: alice.DisplayName, Kind: model.MessageText, Text: "please inspect", Tags: []string{"inspection"}, CreatedAt: now.Add(time.Second)}
	created, inserted, err := store.AppendMessage(ctx, alice, thread.ID, first)
	if err != nil || !inserted || created.Sequence != 1 {
		t.Fatalf("append=%#v inserted=%v err=%v", created, inserted, err)
	}
	duplicate := first
	duplicate.ID = "message-duplicate"
	duplicateResult, inserted, err := store.AppendMessage(ctx, alice, thread.ID, duplicate)
	if err != nil || inserted || duplicateResult.ID != first.ID {
		t.Fatalf("idempotency result=%#v inserted=%v err=%v", duplicateResult, inserted, err)
	}
	conflicting := first
	conflicting.ID, conflicting.Text = "message-conflict", "different"
	if _, _, err := store.AppendMessage(ctx, alice, thread.ID, conflicting); !errors.Is(err, model.ErrConflict) {
		t.Fatalf("reused idempotency key error=%v", err)
	}
	second := model.Message{ID: "message-2", ClientMessageID: "offline-2", SenderID: bob.SubjectID, SenderName: bob.DisplayName, Kind: model.MessageStructured, ContentType: "application/vnd.example.state+json", Data: json.RawMessage(`{"state":"received"}`), Tags: []string{"workflow"}, CreatedAt: now.Add(2 * time.Second)}
	if value, inserted, err := store.AppendMessage(ctx, bob, thread.ID, second); err != nil || !inserted || value.Sequence != 2 {
		t.Fatalf("second=%#v inserted=%v err=%v", value, inserted, err)
	}
	page, err := store.ListMessages(ctx, alice, thread.ID, 0, 1)
	if err != nil || len(page.Messages) != 1 || !page.HasMore || page.NextSequence != 1 {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	search, err := store.SearchMessages(ctx, alice, model.SearchMessagesRequest{Tag: "workflow", Limit: 10})
	if err != nil || len(search) != 1 || search[0].ID != second.ID || string(search[0].Data) != `{"state":"received"}` {
		t.Fatalf("search=%#v err=%v", search, err)
	}
	beforeRead, err := store.SyncState(ctx, bob)
	if err != nil {
		t.Fatal(err)
	}
	cursor, changed, err := store.MarkRead(ctx, alice, thread.ID, 999, now.Add(3*time.Second))
	if err != nil || !changed || cursor.Sequence != 2 {
		t.Fatalf("cursor=%#v changed=%v err=%v", cursor, changed, err)
	}
	afterRead, err := store.SyncState(ctx, bob)
	if err != nil || afterRead.Cursor == beforeRead.Cursor {
		t.Fatalf("receipt change not visible: before=%#v after=%#v err=%v", beforeRead, afterRead, err)
	}
	final, err := store.SyncState(ctx, alice)
	if err != nil || final.Cursor == initial.Cursor || final.UnreadCount != 0 {
		t.Fatalf("initial=%#v final=%#v err=%v", initial, final, err)
	}
}

func TestConcurrentAppendsHaveContinuousSequences(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	actor := model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a"}
	now := time.Now().UTC()
	if _, err := store.CreateThread(ctx, actor, model.Thread{ID: "thread-concurrent", Kind: model.ThreadDirect, MemberIDs: []string{"user-a", "user-b"}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	const count = 20
	results := make(chan int64, count)
	errorsFound := make(chan error, count)
	var wait sync.WaitGroup
	for index := range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			message, created, err := store.AppendMessage(ctx, actor, "thread-concurrent", model.Message{ID: fmt.Sprintf("message-%d", index), ClientMessageID: fmt.Sprintf("client-%d", index), SenderID: actor.SubjectID, Kind: model.MessageText, Text: "hello", CreatedAt: now.Add(time.Duration(index) * time.Millisecond)})
			if err != nil || !created {
				errorsFound <- fmt.Errorf("created=%v: %w", created, err)
				return
			}
			results <- message.Sequence
		}()
	}
	wait.Wait()
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Error(err)
	}
	seen := map[int64]bool{}
	for sequence := range results {
		seen[sequence] = true
	}
	for sequence := int64(1); sequence <= count; sequence++ {
		if !seen[sequence] {
			t.Errorf("missing sequence %d", sequence)
		}
	}
}

func TestOpaqueContextHistoryMembershipAndQuickReplies(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	alice := model.Actor{WorkspaceID: "workspace-a", SubjectID: "alice", DisplayName: "Alice"}
	bob := model.Actor{WorkspaceID: "workspace-a", SubjectID: "bob", DisplayName: "Bob"}
	manager := model.Actor{WorkspaceID: "workspace-a", SubjectID: "manager", DisplayName: "Manager"}
	now := time.Now().UTC()
	contextRef := model.ContextRef{Namespace: "example-product", ResourceType: "asset", ResourceID: "asset-1"}
	thread, err := store.CreateThread(ctx, alice, model.Thread{ID: "thread-context-1", Kind: model.ThreadGroup, Context: &contextRef, Title: "Asset discussion", MemberIDs: []string{"alice", "bob"}, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	message := model.Message{ID: "context-message-1", ClientMessageID: "alice-1", SenderID: alice.SubjectID, SenderName: alice.DisplayName, Kind: model.MessageText, Text: "status update", Tags: []string{"status"}, CreatedAt: now.Add(time.Second)}
	if _, created, err := store.AppendMessage(ctx, alice, thread.ID, message); err != nil || !created {
		t.Fatalf("append created=%v err=%v", created, err)
	}
	if _, err := store.ListMessages(ctx, manager, thread.ID, 0, 10); !errors.Is(err, model.ErrNotMember) {
		t.Fatalf("manager unexpectedly had member access: %v", err)
	}
	history, err := store.ContextHistory(ctx, manager, contextRef, 0, 10)
	if err != nil || history.Thread.ID != thread.ID || len(history.Messages.Messages) != 1 {
		t.Fatalf("history=%#v err=%v", history, err)
	}
	updated, err := store.ReplaceThreadMembers(ctx, alice, thread.ID, []string{"alice", "manager"}, now.Add(2*time.Second))
	if err != nil || fmt.Sprint(updated.MemberIDs) != "[alice manager]" {
		t.Fatalf("updated=%#v err=%v", updated, err)
	}
	if _, err := store.ListMessages(ctx, bob, thread.ID, 0, 10); !errors.Is(err, model.ErrNotMember) {
		t.Fatalf("removed member retained access: %v", err)
	}

	before, err := store.SyncState(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	reply := model.QuickReply{Key: "done", Text: "Done", Tags: []string{"status"}, SortOrder: 10, Enabled: true, UpdatedBy: manager.SubjectID, UpdatedAt: now}
	if _, err := store.PutQuickReply(ctx, manager, reply); err != nil {
		t.Fatal(err)
	}
	after, err := store.SyncState(ctx, alice)
	if err != nil || after.Cursor == before.Cursor {
		t.Fatalf("quick reply sync before=%#v after=%#v err=%v", before, after, err)
	}
	values, err := store.ListQuickReplies(ctx, alice, "status")
	if err != nil || len(values) != 1 || values[0].Key != reply.Key {
		t.Fatalf("quick replies=%#v err=%v", values, err)
	}
}

func TestCreateReplayCannotMutateExistingThread(t *testing.T) {
	store := openStore(t)
	ctx := context.Background()
	actor := model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a"}
	now := time.Now().UTC()
	initial := model.Thread{ID: "thread-group", Kind: model.ThreadGroup, ClientThreadID: "group-1", Title: "Original", MemberIDs: []string{"user-a", "user-b"}, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateThread(ctx, actor, initial); err != nil {
		t.Fatal(err)
	}
	replay := initial
	replay.Title = "Forged rename"
	replay.MemberIDs = []string{"user-a", "user-b", "user-c"}
	loaded, err := store.CreateThread(ctx, actor, replay)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Title != "Original" || len(loaded.MemberIDs) != 2 {
		t.Fatalf("create replay mutated thread: %#v", loaded)
	}
}

func TestSchemaIsSourceOwnedAndPortable(t *testing.T) {
	if err := schemaownership.ValidateAll(imstore.SchemaOwnership()); err != nil {
		t.Fatal(err)
	}
	for _, driver := range []string{"sqlite", "postgres", "mysql"} {
		dialectValue, err := ormdialect.Parse(driver)
		if err != nil {
			t.Fatal(err)
		}
		migrations, err := imstore.SchemaMigrations(dialectValue.WithSchema(""))
		if err != nil || len(migrations) != 1 || len(migrations[0].Statements) < 10 {
			t.Fatalf("driver=%s migrations=%#v err=%v", driver, migrations, err)
		}
	}
}

func openStore(t *testing.T) *imstore.Store {
	t.Helper()
	settings := ormsqlite.DefaultOwnedConnectionConfig(filepath.Join(t.TempDir(), "im.db"))
	dsn, err := settings.DSN()
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := ormsqlite.InitializeOwned(t.Context(), database, settings); err != nil {
		t.Fatal(err)
	}
	dialectValue, err := ormdialect.New(ormdialect.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	renderer := dialectValue.WithSchema("")
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
	store, err := imstore.NewStore(database, renderer, ormsqlite.NewProfile())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}
