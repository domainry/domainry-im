package knowledgefiles

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"testing"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	knowledgefiles "github.com/domainry/domainry-knowledge-sdk/files"
)

type fileServiceStub struct {
	file    knowledgefiles.File
	data    []byte
	binding knowledgefiles.Binding
}

func (value *fileServiceStub) Upload(_ context.Context, authority knowledgefiles.Authority, input knowledgefiles.Upload) (knowledgefiles.File, error) {
	sum := sha256.Sum256(input.Data)
	value.file = knowledgefiles.File{ID: "file_0123456789abcdef0123456789abcdef", WorkspaceID: authority.WorkspaceID, CreatedBy: authority.SubjectID, Filename: input.Filename, ContentType: "image/jpeg", SizeBytes: int64(len(input.Data)), SHA256: hex.EncodeToString(sum[:])}
	value.data = append([]byte(nil), input.Data...)
	return value.file, nil
}
func (value *fileServiceStub) Get(context.Context, knowledgefiles.Authority, string) (knowledgefiles.File, error) {
	return value.file, nil
}
func (value *fileServiceStub) Download(context.Context, knowledgefiles.Authority, string) (knowledgefiles.Download, error) {
	return knowledgefiles.Download{File: value.file, Data: append([]byte(nil), value.data...)}, nil
}
func (value *fileServiceStub) Bind(_ context.Context, _ knowledgefiles.Authority, _ string, binding knowledgefiles.Binding) error {
	value.binding = binding
	return nil
}
func (*fileServiceStub) Delete(context.Context, knowledgefiles.Authority, string) error { return nil }

func TestStoreKeepsKnowledgeFileIDAndIMResourceBinding(t *testing.T) {
	files := &fileServiceStub{}
	store, err := New(files, "runtime-a")
	if err != nil {
		t.Fatal(err)
	}
	actor := model.Actor{WorkspaceID: "workspace-a", SubjectID: "user-a"}
	content := []byte{0xff, 0xd8, 0xff, 0xdb}
	attachment, err := store.Save(t.Context(), actor, "photo.jpg", "image/jpeg", &byteReader{data: content}, 1024)
	if err != nil || attachment.FileID != files.file.ID {
		t.Fatalf("attachment=%+v err=%v", attachment, err)
	}
	if err = store.Validate(t.Context(), actor, attachment); err != nil {
		t.Fatal(err)
	}
	if err = store.Validate(t.Context(), model.Actor{WorkspaceID: actor.WorkspaceID, SubjectID: "user-b"}, attachment); err == nil {
		t.Fatal("another user claimed the unbound upload")
	}
	reader, err := store.Open(t.Context(), model.Actor{WorkspaceID: actor.WorkspaceID, SubjectID: "user-b"}, attachment.FileID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(loaded) != string(content) {
		t.Fatalf("loaded=%x err=%v", loaded, err)
	}
	if err = store.Bind(t.Context(), actor, attachment.FileID, "msg_a", "att_a"); err != nil {
		t.Fatal(err)
	}
	if files.binding.Owner != "im" || files.binding.ResourceType != "message" || files.binding.ResourceID != "msg_a" || files.binding.FieldKey != "att_a" {
		t.Fatalf("binding=%+v", files.binding)
	}
}

type byteReader struct{ data []byte }

func (reader *byteReader) Read(buffer []byte) (int, error) {
	if len(reader.data) == 0 {
		return 0, io.EOF
	}
	n := copy(buffer, reader.data)
	reader.data = reader.data[n:]
	return n, nil
}
