package local

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

type Store struct {
	root string
}

var fileIDPattern = regexp.MustCompile(`^file_[a-f0-9]{32}$`)

func New(root string) (*Store, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("media storage path is required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve media storage path: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o750); err != nil {
		return nil, fmt.Errorf("create media storage path: %w", err)
	}
	return &Store{root: absolute}, nil
}

func (s *Store) Save(ctx context.Context, actor model.Actor, filename, suppliedContentType string, source io.Reader, maxBytes int64) (model.Attachment, error) {
	if source == nil || maxBytes < 1 {
		return model.Attachment{}, errors.New("attachment content is required")
	}
	if err := ctx.Err(); err != nil {
		return model.Attachment{}, err
	}
	head := make([]byte, 512)
	read, err := io.ReadFull(source, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return model.Attachment{}, fmt.Errorf("read attachment header: %w", err)
	}
	head = head[:read]
	if len(head) == 0 {
		return model.Attachment{}, errors.New("attachment is empty")
	}
	contentType := http.DetectContentType(head)
	if detectedMediaType, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = detectedMediaType
	}
	if contentType == "application/octet-stream" {
		if supplied, _, parseErr := mime.ParseMediaType(strings.TrimSpace(suppliedContentType)); parseErr == nil && safeSuppliedContentType(supplied) {
			contentType = supplied
		}
	}
	filename = path.Base(strings.ReplaceAll(strings.TrimSpace(filename), "\\", "/"))
	if filename == "." || filename == "/" {
		filename = "attachment" + extension(contentType)
	}
	workspaceDigest := sha256.Sum256([]byte(actor.WorkspaceID))
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return model.Attachment{}, fmt.Errorf("create attachment object key: %w", err)
	}
	directoryKey := hex.EncodeToString(workspaceDigest[:8])
	directory := filepath.Join(s.root, directoryKey)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return model.Attachment{}, fmt.Errorf("create attachment directory: %w", err)
	}
	fileID := "file_" + hex.EncodeToString(random)
	target := filepath.Join(directory, fileID)
	temporary, err := os.CreateTemp(directory, ".upload-*")
	if err != nil {
		return model.Attachment{}, fmt.Errorf("create temporary attachment: %w", err)
	}
	temporaryPath, committed := temporary.Name(), false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	digest := sha256.New()
	reader := &contextReader{ctx: ctx, reader: io.MultiReader(bytes.NewReader(head), source)}
	written, err := io.Copy(io.MultiWriter(temporary, digest), io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return model.Attachment{}, fmt.Errorf("store attachment: %w", err)
	}
	if written > maxBytes {
		return model.Attachment{}, fmt.Errorf("attachment exceeds %d bytes", maxBytes)
	}
	if err := temporary.Sync(); err != nil {
		return model.Attachment{}, fmt.Errorf("sync attachment: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return model.Attachment{}, fmt.Errorf("close attachment: %w", err)
	}
	if err := os.Rename(temporaryPath, target); err != nil {
		return model.Attachment{}, fmt.Errorf("commit attachment: %w", err)
	}
	committed = true
	return model.Attachment{FileID: fileID, Filename: filename, ContentType: contentType, SizeBytes: written, SHA256: hex.EncodeToString(digest.Sum(nil))}, nil
}

func (s *Store) Open(ctx context.Context, actor model.Actor, fileID string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fileID = strings.TrimSpace(fileID)
	if !fileIDPattern.MatchString(fileID) {
		return nil, errors.New("invalid attachment file ID")
	}
	workspaceDigest := sha256.Sum256([]byte(actor.WorkspaceID))
	target := filepath.Join(s.root, hex.EncodeToString(workspaceDigest[:8]), fileID)
	relative, err := filepath.Rel(s.root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, errors.New("attachment file ID escapes storage root")
	}
	file, err := os.Open(target)
	if err != nil {
		return nil, fmt.Errorf("open attachment: %w", err)
	}
	return file, nil
}

func (s *Store) Validate(ctx context.Context, actor model.Actor, attachment model.Attachment) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := s.Open(ctx, actor, attachment.FileID)
	if err != nil {
		return err
	}
	defer file.Close()
	head := make([]byte, 512)
	read, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("read stored attachment header: %w", err)
	}
	head = head[:read]
	detected := http.DetectContentType(head)
	if detectedMediaType, _, parseErr := mime.ParseMediaType(detected); parseErr == nil {
		detected = detectedMediaType
	}
	digest := sha256.New()
	written, err := io.Copy(digest, &contextReader{ctx: ctx, reader: io.MultiReader(bytes.NewReader(head), file)})
	if err != nil {
		return fmt.Errorf("verify stored attachment: %w", err)
	}
	if detected == "application/octet-stream" && safeSuppliedContentType(attachment.ContentType) {
		detected = attachment.ContentType
	}
	if detected != attachment.ContentType || written != attachment.SizeBytes {
		return errors.New("attachment metadata does not match stored object")
	}
	if attachment.SHA256 != "" && !strings.EqualFold(hex.EncodeToString(digest.Sum(nil)), attachment.SHA256) {
		return errors.New("attachment checksum does not match stored object")
	}
	return nil
}

func (*Store) Bind(context.Context, model.Actor, string, string, string) error { return nil }

func extension(contentType string) string {
	switch contentType {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/zip":
		return ".zip"
	default:
		return ".bin"
	}
}

func safeSuppliedContentType(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "text/html" && value != "application/xhtml+xml" && value != "image/svg+xml" && value != "application/javascript" && value != "text/javascript"
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
