package saas

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	imapplication "github.com/domainry/domainry-im/internal/application/im"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	conversationservice "github.com/domainry/domainry-im/internal/domain/conversation/service"
	localmedia "github.com/domainry/domainry-im/internal/infrastructure/media/local"
	imstore "github.com/domainry/domainry-im/internal/infrastructure/persistence/database/im"
	"github.com/domainry/domainry-im/internal/infrastructure/persistence/migrationhost"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	ormsqlite "github.com/domainry/domainry-orm/sqlite"
	_ "modernc.org/sqlite"
)

func TestRoutesUseExactActionsAndSupportDurableSSECursor(t *testing.T) {
	application := openApplication(t)
	resolver := &actorResolverStub{actor: model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a", DisplayName: "User A"}}
	handler, err := New(application, resolver, 250*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	created := requestJSON(t, handler, http.MethodPost, "/im/threads", map[string]any{"kind": "direct", "member_ids": []string{"user-b"}})
	if created.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var thread model.Thread
	decode(t, created.Body.Bytes(), &thread)
	sent := requestJSON(t, handler, http.MethodPost, "/im/threads/"+thread.ID+"/messages", map[string]any{"client_message_id": "offline-http-1", "kind": "text", "text": "done", "tags": []string{"status"}})
	if sent.Code != http.StatusOK {
		t.Fatalf("send status=%d body=%s", sent.Code, sent.Body.String())
	}
	listed := httptest.NewRecorder()
	handler.ServeHTTP(listed, httptest.NewRequest(http.MethodGet, "/im/threads/"+thread.ID+"/messages?after_sequence=0", nil))
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte("offline-http-1")) {
		t.Fatalf("list status=%d body=%s", listed.Code, listed.Body.String())
	}
	if got := resolver.actionsSnapshot(); len(got) != 3 || got[0] != imapplication.ActionThreadsCreate || got[1] != imapplication.ActionMessagesSend || got[2] != imapplication.ActionMessagesList {
		t.Fatalf("exact actions=%v", got)
	}

	server := httptest.NewServer(handler)
	defer server.Close()
	event, cursor := readFirstEvent(t, server.URL+"/im/events", "")
	if event != "im.sync" || cursor == "" {
		t.Fatalf("first event=%q cursor=%q", event, cursor)
	}
	event, next := readFirstEvent(t, server.URL+"/im/events", cursor)
	if event != "im.ready" || next != cursor {
		t.Fatalf("resume event=%q cursor=%q want=%q", event, next, cursor)
	}
}

func TestTwoAccountsCanHaveACompleteConversation(t *testing.T) {
	alice := model.Actor{WorkspaceID: "workspace-a", SubjectID: "alice", DisplayName: "Alice"}
	bob := model.Actor{WorkspaceID: "workspace-a", SubjectID: "bob", DisplayName: "Bob"}
	application := openApplicationWithDirectory(t, knownDirectory{"alice": true, "bob": true})
	handler, err := New(application, contextActorResolver{}, 250*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	created := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads", map[string]any{"kind": "direct", "member_ids": []string{"bob"}})
	var thread model.Thread
	decode(t, created.Body.Bytes(), &thread)
	if created.Code != http.StatusOK || len(thread.MemberIDs) != 2 {
		t.Fatalf("create status=%d thread=%#v", created.Code, thread)
	}
	input := map[string]any{"client_message_id": "alice-offline-001", "kind": "text", "text": "地下机房检查完成", "tags": []string{"inspection"}}
	sent := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads/"+thread.ID+"/messages", input)
	var first model.Message
	decode(t, sent.Body.Bytes(), &first)
	if sent.Code != http.StatusOK || first.Sequence != 1 || first.SenderID != "alice" {
		t.Fatalf("first status=%d value=%#v", sent.Code, first)
	}
	retried := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads/"+thread.ID+"/messages", input)
	var replay model.Message
	decode(t, retried.Body.Bytes(), &replay)
	if retried.Code != http.StatusOK || replay.ID != first.ID {
		t.Fatalf("retry status=%d first=%#v replay=%#v", retried.Code, first, replay)
	}
	bobThreads := requestAs(t, handler, bob, http.MethodGet, "/im/threads?limit=10", nil)
	var list struct {
		Threads []model.Thread `json:"threads"`
	}
	decode(t, bobThreads.Body.Bytes(), &list)
	if bobThreads.Code != http.StatusOK || len(list.Threads) != 1 || list.Threads[0].UnreadCount != 1 {
		t.Fatalf("Bob threads status=%d values=%#v", bobThreads.Code, list.Threads)
	}
	replied := requestJSONAs(t, handler, bob, http.MethodPost, "/im/threads/"+thread.ID+"/messages", map[string]any{"client_message_id": "bob-online-001", "kind": "text", "text": "收到"})
	var second model.Message
	decode(t, replied.Body.Bytes(), &second)
	if replied.Code != http.StatusOK || second.Sequence != 2 || second.SenderID != "bob" {
		t.Fatalf("reply status=%d value=%#v", replied.Code, second)
	}
	marked := requestJSONAs(t, handler, bob, http.MethodPut, "/im/threads/"+thread.ID+"/read", map[string]any{"sequence": 2})
	var read model.ReadCursor
	decode(t, marked.Body.Bytes(), &read)
	if marked.Code != http.StatusOK || read.Sequence != 2 {
		t.Fatalf("read status=%d value=%#v", marked.Code, read)
	}
	receipts := requestAs(t, handler, alice, http.MethodGet, "/im/threads/"+thread.ID+"/receipts", nil)
	var receiptList struct {
		Receipts []model.ReadCursor `json:"receipts"`
	}
	decode(t, receipts.Body.Bytes(), &receiptList)
	if receipts.Code != http.StatusOK || !hasReceipt(receiptList.Receipts, "bob", 2) {
		t.Fatalf("receipts status=%d values=%#v", receipts.Code, receiptList.Receipts)
	}
}

func TestGenericExtensionsRemainOpaqueToIM(t *testing.T) {
	alice := model.Actor{WorkspaceID: "workspace-a", SubjectID: "alice", DisplayName: "Alice"}
	bob := model.Actor{WorkspaceID: "workspace-a", SubjectID: "bob", DisplayName: "Bob"}
	auditor := model.Actor{WorkspaceID: "workspace-a", SubjectID: "auditor", DisplayName: "Auditor"}
	application := openApplicationWithDirectory(t, knownDirectory{"alice": true, "bob": true, "auditor": true})
	handler, err := New(application, contextActorResolver{}, 250*time.Millisecond, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	plainGroup := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads", map[string]any{
		"kind": "group", "client_thread_id": "operations-room", "member_ids": []string{"bob"},
	})
	var plainGroupThread model.Thread
	decode(t, plainGroup.Body.Bytes(), &plainGroupThread)
	if plainGroup.Code != http.StatusOK || plainGroupThread.ClientThreadID != "operations-room" || plainGroupThread.Context != nil {
		t.Fatalf("plain group status=%d value=%#v", plainGroup.Code, plainGroupThread)
	}
	invalidIdentity := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads", map[string]any{
		"kind": "group", "client_thread_id": "ambiguous", "member_ids": []string{"bob"},
		"context": map[string]any{"namespace": "example-product", "resource_type": "asset", "resource_id": "asset-ambiguous"},
	})
	if invalidIdentity.Code != http.StatusBadRequest {
		t.Fatalf("ambiguous group identity status=%d body=%s", invalidIdentity.Code, invalidIdentity.Body.String())
	}

	replyResponse := requestJSONAs(t, handler, auditor, http.MethodPut, "/im/quick-replies/done", map[string]any{"text": "Done", "tags": []string{"status"}, "sort_order": 10, "enabled": true})
	if replyResponse.Code != http.StatusOK {
		t.Fatalf("put reply status=%d body=%s", replyResponse.Code, replyResponse.Body.String())
	}
	repliesResponse := requestAs(t, handler, alice, http.MethodGet, "/im/quick-replies?tag=status", nil)
	var replies struct {
		QuickReplies []model.QuickReply `json:"quick_replies"`
	}
	decode(t, repliesResponse.Body.Bytes(), &replies)
	if repliesResponse.Code != http.StatusOK || len(replies.QuickReplies) != 1 {
		t.Fatalf("replies status=%d values=%#v", repliesResponse.Code, replies.QuickReplies)
	}

	created := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads", map[string]any{
		"kind": "group", "title": "External resource discussion", "member_ids": []string{"bob"},
		"context": map[string]any{"namespace": "example-product", "resource_type": "asset", "resource_id": "asset-001"},
	})
	var thread model.Thread
	decode(t, created.Body.Bytes(), &thread)
	if created.Code != http.StatusOK || thread.Context == nil || thread.Context.ResourceType != "asset" {
		t.Fatalf("group status=%d value=%#v", created.Code, thread)
	}
	updatedMembers := requestJSONAs(t, handler, auditor, http.MethodPut, "/im/contexts/example-product/asset/asset-001/members", map[string]any{
		"member_ids": []string{"alice", "bob", "auditor"},
	})
	var updatedThread model.Thread
	decode(t, updatedMembers.Body.Bytes(), &updatedThread)
	if updatedMembers.Code != http.StatusOK || len(updatedThread.MemberIDs) != 3 {
		t.Fatalf("context members status=%d value=%#v body=%s", updatedMembers.Code, updatedThread, updatedMembers.Body.String())
	}

	structured := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads/"+thread.ID+"/messages", map[string]any{
		"client_message_id": "external-event-001", "kind": "structured",
		"content_type": "application/vnd.example.estimate-state+json",
		"data":         map[string]any{"state": "received", "external_id": "quote-123"}, "tags": []string{"workflow"},
	})
	var structuredMessage model.Message
	decode(t, structured.Body.Bytes(), &structuredMessage)
	if structured.Code != http.StatusOK || structuredMessage.ContentType != "application/vnd.example.estimate-state+json" || !bytes.Contains(structuredMessage.Data, []byte(`"external_id":"quote-123"`)) {
		t.Fatalf("structured status=%d value=%#v body=%s", structured.Code, structuredMessage, structured.Body.String())
	}

	upload, err := application.UploadAttachment(t.Context(), alice, "machine-room.jpg", "image/jpeg", bytes.NewReader([]byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00, 0x01}))
	if err != nil {
		t.Fatal(err)
	}
	attachmentResponse := requestJSONAs(t, handler, alice, http.MethodPost, "/im/threads/"+thread.ID+"/messages", map[string]any{
		"client_message_id": "attachment-001", "kind": "attachment", "tags": []string{"evidence"},
		"attachments": []map[string]any{{"file_id": upload.FileID, "filename": upload.Filename, "content_type": upload.ContentType, "size_bytes": upload.SizeBytes, "sha256": upload.SHA256, "taken_at": "2026-09-26T08:10:00+08:00", "location_status": "unavailable"}},
	})
	var attachmentMessage model.Message
	decode(t, attachmentResponse.Body.Bytes(), &attachmentMessage)
	if attachmentResponse.Code != http.StatusOK || len(attachmentMessage.Attachments) != 1 {
		t.Fatalf("attachment status=%d value=%#v", attachmentResponse.Code, attachmentMessage)
	}

	historyResponse := requestAs(t, handler, auditor, http.MethodGet, "/im/contexts/example-product/asset/asset-001/history?after_sequence=0&limit=10", nil)
	var history model.ContextHistory
	decode(t, historyResponse.Body.Bytes(), &history)
	if historyResponse.Code != http.StatusOK || history.Thread.ID != thread.ID || len(history.Messages.Messages) != 2 {
		t.Fatalf("history status=%d value=%#v", historyResponse.Code, history)
	}
	searchResponse := requestAs(t, handler, auditor, http.MethodGet, "/im/contexts/example-product/asset/asset-001/history/search?tag=workflow&limit=10", nil)
	var search struct {
		Messages []model.Message `json:"messages"`
	}
	decode(t, searchResponse.Body.Bytes(), &search)
	if searchResponse.Code != http.StatusOK || len(search.Messages) != 1 || search.Messages[0].ID != structuredMessage.ID {
		t.Fatalf("search status=%d values=%#v", searchResponse.Code, search.Messages)
	}
	content := requestAs(t, handler, auditor, http.MethodGet, "/im/contexts/example-product/asset/asset-001/attachments/"+attachmentMessage.Attachments[0].ID+"/content", nil)
	if content.Code != http.StatusOK || content.Body.Len() == 0 || !strings.Contains(content.Header().Get("Content-Disposition"), "machine-room.jpg") {
		t.Fatalf("context attachment status=%d headers=%v", content.Code, content.Header())
	}
	wrongContext := requestAs(t, handler, auditor, http.MethodGet, "/im/contexts/example-product/asset/asset-other/attachments/"+attachmentMessage.Attachments[0].ID+"/content", nil)
	if wrongContext.Code != http.StatusNotFound {
		t.Fatalf("cross-context attachment status=%d body=%s", wrongContext.Code, wrongContext.Body.String())
	}
	legacy := requestJSONAs(t, handler, bob, http.MethodPut, "/im/estimate-requests/old", map[string]any{"status": "received"})
	if legacy.Code != http.StatusNotFound {
		t.Fatalf("business-specific route still exists: status=%d", legacy.Code)
	}
}

func TestUnknownJSONFieldIsRejected(t *testing.T) {
	application := openApplication(t)
	handler, err := New(application, &actorResolverStub{actor: model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a"}}, time.Second, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, handler, http.MethodPost, "/im/threads", map[string]any{"kind": "direct", "member_ids": []string{"user-b"}, "unexpected": true})
	if response.Code != http.StatusBadRequest || !bytes.Contains(response.Body.Bytes(), []byte("im.request_json_invalid")) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

type actorContextKey struct{}

type contextActorResolver struct{}

func (contextActorResolver) Resolve(ctx context.Context, _ string) (model.Actor, error) {
	actor, ok := ctx.Value(actorContextKey{}).(model.Actor)
	if !ok {
		return model.Actor{}, &model.Error{Class: "unauthenticated", Code: "identity.principal_required"}
	}
	return actor, nil
}

type knownDirectory map[string]bool

func (d knownDirectory) ValidateMembers(_ context.Context, memberIDs []string) error {
	for _, memberID := range memberIDs {
		if !d[memberID] {
			return model.Invalid("im.member_unknown")
		}
	}
	return nil
}

func hasReceipt(receipts []model.ReadCursor, subjectID string, sequence int64) bool {
	for _, receipt := range receipts {
		if receipt.SubjectID == subjectID && receipt.Sequence == sequence {
			return true
		}
	}
	return false
}

type actorResolverStub struct {
	mu      sync.Mutex
	actor   model.Actor
	actions []string
}

func (r *actorResolverStub) Resolve(_ context.Context, action string) (model.Actor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.actions = append(r.actions, action)
	return r.actor, nil
}

func (r *actorResolverStub) actionsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.actions...)
}

func openApplication(t *testing.T) *conversationservice.Service {
	return openApplicationWithDirectory(t, allowDirectory{})
}

func openApplicationWithDirectory(t *testing.T, directory conversationservice.MemberDirectory) *conversationservice.Service {
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
	mediaStore, err := localmedia.New(filepath.Join(t.TempDir(), "media"))
	if err != nil {
		t.Fatal(err)
	}
	application, err := conversationservice.New(store, mediaStore, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = application.Close() })
	return application
}

type allowDirectory struct{}

func (allowDirectory) ValidateMembers(context.Context, []string) error { return nil }

func requestJSON(t *testing.T, handler http.Handler, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(method, path, bytes.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func requestJSONAs(t *testing.T, handler http.Handler, actor model.Actor, method, path string, value any) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return requestAs(t, handler, actor, method, path, payload)
}

func requestAs(t *testing.T, handler http.Handler, actor model.Actor, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var request *http.Request
	if body == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
	}
	request = request.WithContext(context.WithValue(request.Context(), actorContextKey{}, actor))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func decode(t *testing.T, raw []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(raw, target); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
}

func readFirstEvent(t *testing.T, url, cursor string) (string, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cursor != "" {
		request.Header.Set("Last-Event-ID", cursor)
	}
	response, err := (&http.Client{Timeout: 2 * time.Second}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("SSE status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	scanner := bufio.NewScanner(response.Body)
	event, id := "", ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "id: ") {
			id = strings.TrimPrefix(line, "id: ")
		}
		if line == "" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return event, id
}
