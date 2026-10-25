// Package knowledgefiles adapts the neutral Knowledge file service to the IM
// attachment port. IM owns message access; Knowledge owns bytes and file
// lifecycle metadata.
package knowledgefiles

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	knowledgefiles "github.com/domainry/domainry-knowledge-sdk/files"
)

type Store struct {
	service   knowledgefiles.Service
	runtimeID string
}

func New(service knowledgefiles.Service, runtimeID string) (*Store, error) {
	runtimeID = strings.TrimSpace(runtimeID)
	if service == nil || runtimeID == "" {
		return nil, errors.New("Knowledge file service and Runtime ID are required")
	}
	return &Store{service: service, runtimeID: runtimeID}, nil
}

func (store *Store) Save(ctx context.Context, actor model.Actor, filename, contentType string, source io.Reader, maxBytes int64) (model.Attachment, error) {
	if source == nil || maxBytes < 1 || maxBytes > knowledgefiles.MaxContentBytes {
		return model.Attachment{}, errors.New("attachment upload limit is invalid")
	}
	content, sizeBytes, digest, cleanup, err := prepareUploadStream(ctx, source, maxBytes)
	if err != nil {
		return model.Attachment{}, err
	}
	defer cleanup()
	random := make([]byte, 16)
	if _, err = rand.Read(random); err != nil {
		return model.Attachment{}, err
	}
	value, err := store.service.UploadStream(ctx, store.authority(actor), knowledgefiles.StreamUpload{
		ClientID: "im-upload-" + hex.EncodeToString(random), Filename: filename, ContentType: contentType,
		SizeBytes: sizeBytes, SHA256: digest, Content: content,
	})
	if err != nil {
		return model.Attachment{}, err
	}
	return model.Attachment{FileID: value.ID, Filename: value.Filename, ContentType: value.ContentType, SizeBytes: value.SizeBytes, SHA256: value.SHA256}, nil
}

func prepareUploadStream(ctx context.Context, source io.Reader, maxBytes int64) (io.Reader, int64, string, func(), error) {
	digest := sha256.New()
	copySource := &contextReader{ctx: ctx, reader: io.LimitReader(source, maxBytes+1)}
	if seeker, ok := source.(io.ReadSeeker); ok {
		sizeBytes, err := io.Copy(digest, copySource)
		if err != nil {
			return nil, 0, "", func() {}, err
		}
		if sizeBytes == 0 || sizeBytes > maxBytes {
			return nil, 0, "", func() {}, errors.New("attachment size is invalid")
		}
		if _, err = seeker.Seek(0, io.SeekStart); err != nil {
			return nil, 0, "", func() {}, err
		}
		return &contextReader{ctx: ctx, reader: io.LimitReader(seeker, sizeBytes)}, sizeBytes, hex.EncodeToString(digest.Sum(nil)), func() {}, nil
	}
	temporary, err := os.CreateTemp("", "domainry-im-upload-*")
	if err != nil {
		return nil, 0, "", func() {}, err
	}
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}
	sizeBytes, err := io.Copy(io.MultiWriter(temporary, digest), copySource)
	if err != nil {
		cleanup()
		return nil, 0, "", func() {}, err
	}
	if sizeBytes == 0 || sizeBytes > maxBytes {
		cleanup()
		return nil, 0, "", func() {}, errors.New("attachment size is invalid")
	}
	if _, err = temporary.Seek(0, io.SeekStart); err != nil {
		cleanup()
		return nil, 0, "", func() {}, err
	}
	return &contextReader{ctx: ctx, reader: io.LimitReader(temporary, sizeBytes)}, sizeBytes, hex.EncodeToString(digest.Sum(nil)), cleanup, nil
}

func (store *Store) Open(ctx context.Context, actor model.Actor, fileID string) (io.ReadCloser, error) {
	value, err := store.service.Download(ctx, store.authority(actor), strings.TrimSpace(fileID))
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(value.Data)), nil
}

func (store *Store) Validate(ctx context.Context, actor model.Actor, attachment model.Attachment) error {
	value, err := store.service.Get(ctx, store.authority(actor), strings.TrimSpace(attachment.FileID))
	if err != nil {
		return err
	}
	if value.WorkspaceID != actor.WorkspaceID || value.CreatedBy != actor.SubjectID || value.ID != attachment.FileID || value.Filename != attachment.Filename || value.ContentType != attachment.ContentType || value.SizeBytes != attachment.SizeBytes || !strings.EqualFold(value.SHA256, attachment.SHA256) {
		return errors.New("attachment metadata does not match Knowledge file")
	}
	return nil
}

func (store *Store) Bind(ctx context.Context, actor model.Actor, fileID, messageID, attachmentID string) error {
	return store.service.Bind(ctx, store.authority(actor), strings.TrimSpace(fileID), knowledgefiles.Binding{
		Owner: "im", ResourceType: "message", ResourceID: messageID, FieldKey: attachmentID,
	})
}

func (store *Store) authority(actor model.Actor) knowledgefiles.Authority {
	return knowledgefiles.Authority{RuntimeID: store.runtimeID, WorkspaceID: actor.WorkspaceID, SubjectID: actor.SubjectID}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *contextReader) Read(buffer []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	return reader.reader.Read(buffer)
}
