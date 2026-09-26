package local

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

func TestStoreValidatesContentWorkspaceAndTraversal(t *testing.T) {
	store, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	content := []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43, 0x00, 0x01}
	actor := model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a"}
	attachment, err := store.Save(context.Background(), actor, "photo.exe", "application/octet-stream", bytes.NewReader(content), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Validate(context.Background(), actor, attachment); err != nil {
		t.Fatal(err)
	}
	if err := store.Validate(context.Background(), model.Actor{WorkspaceID: "workspace-b", SubjectID: "user-b"}, attachment); err == nil {
		t.Fatal("cross-workspace attachment was accepted")
	}
	file, err := store.Open(context.Background(), actor, attachment.FileID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := io.ReadAll(file)
	_ = file.Close()
	if err != nil || !bytes.Equal(loaded, content) {
		t.Fatalf("loaded=%x err=%v", loaded, err)
	}
	textAttachment, err := store.Save(context.Background(), actor, "notes.txt", "text/plain", strings.NewReader("plain text"), 1024)
	if err != nil || textAttachment.ContentType != "text/plain" || textAttachment.Filename != "notes.txt" {
		t.Fatalf("generic text attachment=%#v err=%v", textAttachment, err)
	}
	if _, err := store.Open(context.Background(), actor, "../outside.jpg"); err == nil {
		t.Fatal("path traversal was accepted")
	}
}
