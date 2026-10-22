package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"mime"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/domainry/domainry-foundation/entityid"
	"github.com/domainry/domainry-im/internal/domain/conversation/model"
)

const (
	maxThreadMembers     = 200
	maxMessageBytes      = 10 * 1024
	maxStructuredBytes   = 32 * 1024
	maxAttachments       = 8
	maxAttachmentBytes   = 25 * 1024 * 1024
	maxTags              = 16
	maxQuickReplyBytes   = 200
	maxAttachmentNameLen = 255
)

var stableID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,254}$`)
var stableClientID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
var extensionType = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/+:-]{0,127}$`)
var tagID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,63}$`)
var sha256Hex = regexp.MustCompile(`^[a-fA-F0-9]{64}$`)

type Repository interface {
	CreateThread(context.Context, model.Actor, model.Thread) (model.Thread, error)
	ReplaceThreadMembers(context.Context, model.Actor, string, []string, time.Time) (model.Thread, error)
	ListThreads(context.Context, model.Actor, int) ([]model.Thread, error)
	AppendMessage(context.Context, model.Actor, string, model.Message) (model.Message, bool, error)
	ListMessages(context.Context, model.Actor, string, int64, int) (model.MessagePage, error)
	SearchMessages(context.Context, model.Actor, model.SearchMessagesRequest) ([]model.Message, error)
	ListReadCursors(context.Context, model.Actor, string) ([]model.ReadCursor, error)
	MarkRead(context.Context, model.Actor, string, int64, time.Time) (model.ReadCursor, bool, error)
	GetAttachment(context.Context, model.Actor, string) (model.Attachment, error)
	ContextHistory(context.Context, model.Actor, model.ContextRef, int64, int) (model.ContextHistory, error)
	SearchContextHistory(context.Context, model.Actor, model.ContextRef, model.SearchMessagesRequest) ([]model.Message, error)
	GetContextAttachment(context.Context, model.Actor, string) (model.Attachment, error)
	ListQuickReplies(context.Context, model.Actor, string) ([]model.QuickReply, error)
	GetQuickReply(context.Context, model.Actor, string) (model.QuickReply, error)
	PutQuickReply(context.Context, model.Actor, model.QuickReply) (model.QuickReply, error)
	SyncState(context.Context, model.Actor) (model.SyncState, error)
	Ready(context.Context) error
	Close() error
}

type AttachmentStorage interface {
	Save(context.Context, model.Actor, string, string, io.Reader, int64) (model.Attachment, error)
	Open(context.Context, model.Actor, string) (io.ReadCloser, error)
	Validate(context.Context, model.Actor, model.Attachment) error
	Bind(context.Context, model.Actor, string, string, string) error
}

type MemberDirectory interface {
	ValidateMembers(context.Context, []string) error
}

type Service struct {
	repository  Repository
	attachments AttachmentStorage
	directory   MemberDirectory
	now         func() time.Time
}

func New(repository Repository, attachments AttachmentStorage, directory MemberDirectory) (*Service, error) {
	if repository == nil || directory == nil {
		return nil, &model.Error{Class: "internal", Code: "im.configuration_invalid"}
	}
	return &Service{repository: repository, attachments: attachments, directory: directory, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) CreateThread(ctx context.Context, actor model.Actor, request model.CreateThreadRequest) (model.Thread, error) {
	if err := validateActor(actor); err != nil {
		return model.Thread{}, err
	}
	request.Kind = model.ThreadKind(strings.TrimSpace(string(request.Kind)))
	request.ClientThreadID, request.Title = strings.TrimSpace(request.ClientThreadID), strings.TrimSpace(request.Title)
	if len(request.Title) > 200 {
		return model.Thread{}, model.Invalid("im.thread_invalid")
	}
	members, err := normalizeMembers(actor.SubjectID, request.MemberIDs)
	if err != nil {
		return model.Thread{}, err
	}
	var contextRef *model.ContextRef
	switch request.Kind {
	case model.ThreadDirect:
		if request.ClientThreadID != "" || request.Context != nil || len(members) != 2 {
			return model.Thread{}, model.Invalid("im.direct_thread_invalid")
		}
	case model.ThreadGroup:
		if (request.ClientThreadID == "") == (request.Context == nil) {
			return model.Thread{}, model.Invalid("im.group_identity_invalid")
		}
		if request.ClientThreadID != "" && !stableClientID.MatchString(request.ClientThreadID) {
			return model.Thread{}, model.Invalid("im.group_identity_invalid")
		}
		if request.Context != nil {
			value, err := normalizeContext(*request.Context)
			if err != nil {
				return model.Thread{}, err
			}
			contextRef = &value
		}
	default:
		return model.Thread{}, model.Invalid("im.thread_kind_invalid")
	}
	if err := s.directory.ValidateMembers(ctx, members); err != nil {
		return model.Thread{}, err
	}
	threadID, err := deterministicThreadID(actor.WorkspaceID, request.Kind, request.ClientThreadID, contextRef, members)
	if err != nil {
		return model.Thread{}, err
	}
	now := s.now()
	thread := model.Thread{
		ID:   threadID,
		Kind: request.Kind, ClientThreadID: request.ClientThreadID, Context: contextRef,
		Title: request.Title, MemberIDs: members, CreatedAt: now, UpdatedAt: now,
	}
	result, err := s.repository.CreateThread(ctx, actor, thread)
	return result, model.RepositoryError(err)
}

func (s *Service) ListThreads(ctx context.Context, actor model.Actor, limit int) ([]model.Thread, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	limit, err := normalizeLimit(limit)
	if err != nil {
		return nil, err
	}
	values, err := s.repository.ListThreads(ctx, actor, limit)
	return values, model.RepositoryError(err)
}

func (s *Service) ReplaceThreadMembers(ctx context.Context, actor model.Actor, threadID string, request model.ReplaceThreadMembersRequest) (model.Thread, error) {
	if err := validateActor(actor); err != nil {
		return model.Thread{}, err
	}
	threadID = strings.TrimSpace(threadID)
	if !stableID.MatchString(threadID) {
		return model.Thread{}, model.Invalid("im.thread_invalid")
	}
	members, err := normalizeMemberIDs(request.MemberIDs)
	if err != nil {
		return model.Thread{}, err
	}
	if err := s.directory.ValidateMembers(ctx, members); err != nil {
		return model.Thread{}, err
	}
	value, err := s.repository.ReplaceThreadMembers(ctx, actor, threadID, members, s.now())
	return value, model.RepositoryError(err)
}

func (s *Service) SendMessage(ctx context.Context, actor model.Actor, threadID string, request model.CreateMessageRequest) (model.Message, error) {
	if err := validateActor(actor); err != nil {
		return model.Message{}, err
	}
	threadID, request.ClientMessageID = strings.TrimSpace(threadID), strings.TrimSpace(request.ClientMessageID)
	request.Text, request.TemplateKey = strings.TrimSpace(request.Text), strings.TrimSpace(request.TemplateKey)
	request.ContentType = strings.TrimSpace(request.ContentType)
	request.Kind = model.MessageKind(strings.TrimSpace(string(request.Kind)))
	if !stableID.MatchString(threadID) || !stableClientID.MatchString(request.ClientMessageID) || len([]byte(request.Text)) > maxMessageBytes {
		return model.Message{}, model.Invalid("im.message_invalid")
	}
	tags, err := normalizeTags(request.Tags)
	if err != nil {
		return model.Message{}, err
	}
	data := json.RawMessage(nil)
	switch request.Kind {
	case model.MessageText:
		if request.Text == "" || request.TemplateKey != "" || request.ContentType != "" || len(request.Data) != 0 || len(request.Attachments) != 0 {
			return model.Message{}, model.Invalid("im.message_content_invalid")
		}
	case model.MessageTemplate:
		if request.Text != "" || !stableID.MatchString(request.TemplateKey) || request.ContentType != "" || len(request.Data) != 0 || len(request.Attachments) != 0 {
			return model.Message{}, model.Invalid("im.message_content_invalid")
		}
		reply, err := s.repository.GetQuickReply(ctx, actor, request.TemplateKey)
		if err != nil {
			return model.Message{}, model.RepositoryError(err)
		}
		if !reply.Enabled {
			return model.Message{}, &model.Error{Class: "not_found", Code: "im.quick_reply_not_found"}
		}
		request.Text = reply.Text
		tags, err = normalizeTags(append(append([]string(nil), reply.Tags...), tags...))
		if err != nil {
			return model.Message{}, err
		}
	case model.MessageAttachment:
		if request.TemplateKey != "" || request.ContentType != "" || len(request.Data) != 0 || len(request.Attachments) == 0 || len(request.Attachments) > maxAttachments {
			return model.Message{}, model.Invalid("im.message_content_invalid")
		}
	case model.MessageStructured:
		if request.Text != "" || request.TemplateKey != "" || !extensionType.MatchString(request.ContentType) || len(request.Data) == 0 || len(request.Data) > maxStructuredBytes || len(request.Attachments) > maxAttachments {
			return model.Message{}, model.Invalid("im.message_content_invalid")
		}
		data, err = compactJSON(request.Data)
		if err != nil {
			return model.Message{}, model.Invalid("im.message_data_invalid")
		}
	default:
		return model.Message{}, model.Invalid("im.message_kind_invalid")
	}
	attachments := make([]model.Attachment, 0, len(request.Attachments))
	for _, value := range request.Attachments {
		attachment, err := normalizeAttachment(value)
		if err != nil {
			return model.Message{}, err
		}
		if s.attachments == nil {
			return model.Message{}, &model.Error{Class: "unavailable", Code: "im.attachments_unavailable"}
		}
		if err := s.attachments.Validate(ctx, actor, attachment); err != nil {
			return model.Message{}, model.Invalid("im.attachment_object_invalid")
		}
		attachments = append(attachments, attachment)
	}
	messageID, err := newID("immsg")
	if err != nil {
		return model.Message{}, err
	}
	message := model.Message{
		ID: messageID, ThreadID: threadID, ClientMessageID: request.ClientMessageID,
		SenderID: actor.SubjectID, SenderName: actor.DisplayName, Kind: request.Kind,
		Text: request.Text, TemplateKey: request.TemplateKey, ContentType: request.ContentType,
		Data: data, Tags: tags, Attachments: attachments, CreatedAt: s.now(),
	}
	result, _, err := s.repository.AppendMessage(ctx, actor, threadID, message)
	if err != nil {
		return model.Message{}, model.RepositoryError(err)
	}
	for _, attachment := range result.Attachments {
		if err = s.attachments.Bind(ctx, actor, attachment.FileID, result.ID, attachment.ID); err != nil {
			return model.Message{}, &model.Error{Class: "unavailable", Code: "im.attachment_binding_failed", Cause: err}
		}
	}
	return result, nil
}

func (s *Service) ListMessages(ctx context.Context, actor model.Actor, threadID string, afterSequence int64, limit int) (model.MessagePage, error) {
	if err := validateActor(actor); err != nil {
		return model.MessagePage{}, err
	}
	if !stableID.MatchString(strings.TrimSpace(threadID)) || afterSequence < 0 {
		return model.MessagePage{}, model.Invalid("im.message_cursor_invalid")
	}
	limit, err := normalizeLimit(limit)
	if err != nil {
		return model.MessagePage{}, err
	}
	page, err := s.repository.ListMessages(ctx, actor, threadID, afterSequence, limit)
	return page, model.RepositoryError(err)
}

func (s *Service) SearchMessages(ctx context.Context, actor model.Actor, request model.SearchMessagesRequest) ([]model.Message, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	request.ThreadID, request.Query, request.Tag = strings.TrimSpace(request.ThreadID), strings.TrimSpace(request.Query), strings.TrimSpace(request.Tag)
	if request.ThreadID == "" && request.Query == "" && request.Tag == "" {
		return nil, model.Invalid("im.search_filter_required")
	}
	if request.ThreadID != "" && !stableID.MatchString(request.ThreadID) || len(request.Query) > 200 || request.Tag != "" && !tagID.MatchString(request.Tag) {
		return nil, model.Invalid("im.search_filter_invalid")
	}
	limit, err := normalizeLimit(request.Limit)
	if err != nil {
		return nil, err
	}
	request.Limit = limit
	values, err := s.repository.SearchMessages(ctx, actor, request)
	return values, model.RepositoryError(err)
}

func (s *Service) ListReadCursors(ctx context.Context, actor model.Actor, threadID string) ([]model.ReadCursor, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if !stableID.MatchString(strings.TrimSpace(threadID)) {
		return nil, model.Invalid("im.thread_invalid")
	}
	values, err := s.repository.ListReadCursors(ctx, actor, threadID)
	return values, model.RepositoryError(err)
}

func (s *Service) MarkRead(ctx context.Context, actor model.Actor, threadID string, request model.ReadRequest) (model.ReadCursor, error) {
	if err := validateActor(actor); err != nil {
		return model.ReadCursor{}, err
	}
	if !stableID.MatchString(strings.TrimSpace(threadID)) || request.Sequence < 0 {
		return model.ReadCursor{}, model.Invalid("im.read_cursor_invalid")
	}
	value, _, err := s.repository.MarkRead(ctx, actor, threadID, request.Sequence, s.now())
	return value, model.RepositoryError(err)
}

func (s *Service) ContextHistory(ctx context.Context, actor model.Actor, contextRef model.ContextRef, afterSequence int64, limit int) (model.ContextHistory, error) {
	if err := validateActor(actor); err != nil {
		return model.ContextHistory{}, err
	}
	contextRef, err := normalizeContext(contextRef)
	if err != nil || afterSequence < 0 {
		return model.ContextHistory{}, model.Invalid("im.context_invalid")
	}
	limit, err = normalizeLimit(limit)
	if err != nil {
		return model.ContextHistory{}, err
	}
	value, err := s.repository.ContextHistory(ctx, actor, contextRef, afterSequence, limit)
	return value, model.RepositoryError(err)
}

func (s *Service) SearchContextHistory(ctx context.Context, actor model.Actor, contextRef model.ContextRef, request model.SearchMessagesRequest) ([]model.Message, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	contextRef, err := normalizeContext(contextRef)
	if err != nil {
		return nil, err
	}
	request.ThreadID = ""
	request.Query, request.Tag = strings.TrimSpace(request.Query), strings.TrimSpace(request.Tag)
	if request.Query == "" && request.Tag == "" {
		return nil, model.Invalid("im.search_filter_required")
	}
	if len(request.Query) > 200 || request.Tag != "" && !tagID.MatchString(request.Tag) {
		return nil, model.Invalid("im.search_filter_invalid")
	}
	request.Limit, err = normalizeLimit(request.Limit)
	if err != nil {
		return nil, err
	}
	values, err := s.repository.SearchContextHistory(ctx, actor, contextRef, request)
	return values, model.RepositoryError(err)
}

func (s *Service) ListQuickReplies(ctx context.Context, actor model.Actor, tag string) ([]model.QuickReply, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	tag = strings.TrimSpace(tag)
	if tag != "" && !tagID.MatchString(tag) {
		return nil, model.Invalid("im.tag_invalid")
	}
	values, err := s.repository.ListQuickReplies(ctx, actor, tag)
	return values, model.RepositoryError(err)
}

func (s *Service) PutQuickReply(ctx context.Context, actor model.Actor, key string, request model.PutQuickReplyRequest) (model.QuickReply, error) {
	if err := validateActor(actor); err != nil {
		return model.QuickReply{}, err
	}
	key, request.Text = strings.TrimSpace(key), strings.TrimSpace(request.Text)
	tags, err := normalizeTags(request.Tags)
	if err != nil || !stableID.MatchString(key) || len(key) > 64 || request.Text == "" || len([]byte(request.Text)) > maxQuickReplyBytes || request.SortOrder < 0 || request.SortOrder > 10000 {
		return model.QuickReply{}, model.Invalid("im.quick_reply_invalid")
	}
	value := model.QuickReply{Key: key, Text: request.Text, Tags: tags, SortOrder: request.SortOrder, Enabled: request.Enabled, UpdatedBy: actor.SubjectID, UpdatedAt: s.now()}
	result, err := s.repository.PutQuickReply(ctx, actor, value)
	return result, model.RepositoryError(err)
}

func (s *Service) SyncState(ctx context.Context, actor model.Actor) (model.SyncState, error) {
	if err := validateActor(actor); err != nil {
		return model.SyncState{}, err
	}
	value, err := s.repository.SyncState(ctx, actor)
	return value, model.RepositoryError(err)
}

func (s *Service) UploadAttachment(ctx context.Context, actor model.Actor, filename, contentType string, source io.Reader) (model.CreateAttachmentRequest, error) {
	if err := validateActor(actor); err != nil {
		return model.CreateAttachmentRequest{}, err
	}
	if s.attachments == nil {
		return model.CreateAttachmentRequest{}, &model.Error{Class: "unavailable", Code: "im.attachments_unavailable"}
	}
	if source == nil {
		return model.CreateAttachmentRequest{}, model.Invalid("im.attachment_invalid")
	}
	attachment, err := s.attachments.Save(ctx, actor, strings.TrimSpace(filename), strings.TrimSpace(contentType), source, maxAttachmentBytes)
	if err != nil {
		return model.CreateAttachmentRequest{}, &model.Error{Class: "bad_request", Code: "im.attachment_upload_invalid", Cause: err}
	}
	return model.CreateAttachmentRequest{FileID: attachment.FileID, Filename: attachment.Filename, ContentType: attachment.ContentType, SizeBytes: attachment.SizeBytes, SHA256: attachment.SHA256}, nil
}

func (s *Service) OpenAttachment(ctx context.Context, actor model.Actor, attachmentID string) (model.Attachment, io.ReadCloser, error) {
	if err := validateActor(actor); err != nil {
		return model.Attachment{}, nil, err
	}
	attachmentID = strings.TrimSpace(attachmentID)
	if !stableID.MatchString(attachmentID) {
		return model.Attachment{}, nil, model.Invalid("im.attachment_invalid")
	}
	attachment, err := s.repository.GetAttachment(ctx, actor, attachmentID)
	if err != nil {
		return model.Attachment{}, nil, model.RepositoryError(err)
	}
	return s.openStoredAttachment(ctx, actor, attachment)
}

func (s *Service) OpenContextAttachment(ctx context.Context, actor model.Actor, attachmentID string) (model.Attachment, io.ReadCloser, error) {
	if err := validateActor(actor); err != nil {
		return model.Attachment{}, nil, err
	}
	attachmentID = strings.TrimSpace(attachmentID)
	if !stableID.MatchString(attachmentID) {
		return model.Attachment{}, nil, model.Invalid("im.attachment_invalid")
	}
	attachment, err := s.repository.GetContextAttachment(ctx, actor, attachmentID)
	if err != nil {
		return model.Attachment{}, nil, model.RepositoryError(err)
	}
	return s.openStoredAttachment(ctx, actor, attachment)
}

func (s *Service) openStoredAttachment(ctx context.Context, actor model.Actor, attachment model.Attachment) (model.Attachment, io.ReadCloser, error) {
	if s.attachments == nil {
		return model.Attachment{}, nil, &model.Error{Class: "unavailable", Code: "im.attachments_unavailable"}
	}
	content, err := s.attachments.Open(ctx, actor, attachment.FileID)
	if err != nil {
		return model.Attachment{}, nil, &model.Error{Class: "internal", Code: "im.attachment_storage_failed", Cause: err}
	}
	return attachment, content, nil
}

func (s *Service) Ready(ctx context.Context) error { return s.repository.Ready(ctx) }
func (s *Service) Close() error                    { return s.repository.Close() }

func validateActor(actor model.Actor) error {
	if !stableID.MatchString(strings.TrimSpace(actor.WorkspaceID)) || !stableID.MatchString(strings.TrimSpace(actor.SubjectID)) {
		return &model.Error{Class: "forbidden", Code: "im.actor_invalid"}
	}
	return nil
}

func normalizeLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 1 || limit > 100 {
		return 0, model.Invalid("im.limit_invalid")
	}
	return limit, nil
}

func normalizeContext(value model.ContextRef) (model.ContextRef, error) {
	value.Namespace = strings.TrimSpace(value.Namespace)
	value.ResourceType = strings.TrimSpace(value.ResourceType)
	value.ResourceID = strings.TrimSpace(value.ResourceID)
	if !stableID.MatchString(value.Namespace) || !stableID.MatchString(value.ResourceType) || !stableID.MatchString(value.ResourceID) {
		return model.ContextRef{}, model.Invalid("im.context_invalid")
	}
	return value, nil
}

func normalizeMembers(actor string, values []string) ([]string, error) {
	values = append(append([]string(nil), values...), actor)
	return normalizeMemberIDs(values)
}

func normalizeMemberIDs(values []string) ([]string, error) {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !stableID.MatchString(value) {
			return nil, model.Invalid("im.member_invalid")
		}
		set[value] = true
	}
	if len(set) == 0 || len(set) > maxThreadMembers {
		return nil, model.Invalid("im.member_limit_exceeded")
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeTags(values []string) ([]string, error) {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if !tagID.MatchString(value) {
			return nil, model.Invalid("im.tag_invalid")
		}
		set[value] = true
	}
	if len(set) > maxTags {
		return nil, model.Invalid("im.tag_limit_exceeded")
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func normalizeAttachment(value model.CreateAttachmentRequest) (model.Attachment, error) {
	value.FileID, value.ContentType = strings.TrimSpace(value.FileID), strings.TrimSpace(value.ContentType)
	value.Filename = path.Base(strings.ReplaceAll(strings.TrimSpace(value.Filename), "\\", "/"))
	if value.Filename == "." {
		value.Filename = ""
	}
	value.SHA256, value.TakenAt = strings.TrimSpace(value.SHA256), strings.TrimSpace(value.TakenAt)
	mediaType, _, err := mime.ParseMediaType(value.ContentType)
	if err != nil || !stableID.MatchString(value.FileID) || len(value.FileID) > 96 || len(value.Filename) > maxAttachmentNameLen || value.SizeBytes < 1 || value.SizeBytes > maxAttachmentBytes {
		return model.Attachment{}, model.Invalid("im.attachment_invalid")
	}
	if !sha256Hex.MatchString(value.SHA256) {
		return model.Attachment{}, model.Invalid("im.attachment_checksum_invalid")
	}
	if value.TakenAt != "" {
		if _, err := time.Parse(time.RFC3339, value.TakenAt); err != nil {
			return model.Attachment{}, model.Invalid("im.attachment_taken_at_invalid")
		}
	}
	if (value.Latitude != nil && (math.IsNaN(*value.Latitude) || math.IsInf(*value.Latitude, 0) || *value.Latitude < -90 || *value.Latitude > 90)) || (value.Longitude != nil && (math.IsNaN(*value.Longitude) || math.IsInf(*value.Longitude, 0) || *value.Longitude < -180 || *value.Longitude > 180)) || (value.Latitude == nil) != (value.Longitude == nil) || (value.LocationAccuracyMeters != nil && (math.IsNaN(*value.LocationAccuracyMeters) || math.IsInf(*value.LocationAccuracyMeters, 0) || *value.LocationAccuracyMeters < 0)) {
		return model.Attachment{}, model.Invalid("im.attachment_location_invalid")
	}
	switch value.LocationStatus {
	case "":
		if value.Latitude != nil || value.Longitude != nil || value.LocationAccuracyMeters != nil {
			return model.Attachment{}, model.Invalid("im.attachment_location_invalid")
		}
	case model.LocationCaptured:
		if value.Latitude == nil || value.Longitude == nil {
			return model.Attachment{}, model.Invalid("im.attachment_location_invalid")
		}
	case model.LocationUnavailable, model.LocationPermissionDenied:
		if value.Latitude != nil || value.Longitude != nil || value.LocationAccuracyMeters != nil {
			return model.Attachment{}, model.Invalid("im.attachment_location_invalid")
		}
	default:
		return model.Attachment{}, model.Invalid("im.attachment_location_status_invalid")
	}
	attachmentID, err := newID("imatt")
	if err != nil {
		return model.Attachment{}, err
	}
	return model.Attachment{ID: attachmentID, FileID: value.FileID, Filename: value.Filename, ContentType: mediaType, SizeBytes: value.SizeBytes, SHA256: strings.ToLower(value.SHA256), TakenAt: value.TakenAt, LocationStatus: value.LocationStatus, Latitude: value.Latitude, Longitude: value.Longitude, LocationAccuracyMeters: value.LocationAccuracyMeters}, nil
}

func compactJSON(value json.RawMessage) (json.RawMessage, error) {
	var output bytes.Buffer
	if err := json.Compact(&output, value); err != nil {
		return nil, err
	}
	return json.RawMessage(append([]byte(nil), output.Bytes()...)), nil
}

func deterministicThreadID(workspaceID string, kind model.ThreadKind, clientThreadID string, contextRef *model.ContextRef, members []string) (string, error) {
	identity := strings.Join(members, "\x00")
	if kind == model.ThreadGroup {
		identity = "client\x00" + clientThreadID
		if contextRef != nil {
			identity = "context\x00" + contextRef.Namespace + "\x00" + contextRef.ResourceType + "\x00" + contextRef.ResourceID
		}
	}
	value, err := entityid.Derive("imthr", "im.thread", workspaceID, string(kind), identity)
	if err != nil {
		return "", &model.Error{Class: "unavailable", Code: "im.identifier_unavailable", Cause: err}
	}
	return value, nil
}

func newID(prefix string) (string, error) {
	value, err := entityid.New(prefix)
	if err != nil {
		return "", &model.Error{Class: "unavailable", Code: "im.identifier_unavailable", Cause: err}
	}
	return value, nil
}
