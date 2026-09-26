package model

import (
	"encoding/json"
	"time"
)

// ThreadKind deliberately contains only messaging concepts. Product-specific
// group types belong in ContextRef, not in the IM component's type system.
type ThreadKind string

const (
	ThreadDirect ThreadKind = "direct"
	ThreadGroup  ThreadKind = "group"
)

type MessageKind string

const (
	MessageText       MessageKind = "text"
	MessageAttachment MessageKind = "attachment"
	MessageTemplate   MessageKind = "template"
	MessageStructured MessageKind = "structured"
)

type LocationStatus string

const (
	LocationCaptured         LocationStatus = "captured"
	LocationUnavailable      LocationStatus = "unavailable"
	LocationPermissionDenied LocationStatus = "permission_denied"
)

type Actor struct {
	WorkspaceID string
	SubjectID   string
	DisplayName string
}

// ContextRef is an opaque link owned by an integrating product. IM stores and
// indexes it, but never interprets its namespace, resource type, or identifier.
type ContextRef struct {
	Namespace    string `json:"namespace"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
}

type CreateThreadRequest struct {
	Kind           ThreadKind  `json:"kind"`
	ClientThreadID string      `json:"client_thread_id,omitempty"`
	Context        *ContextRef `json:"context,omitempty"`
	Title          string      `json:"title,omitempty"`
	MemberIDs      []string    `json:"member_ids"`
}

type ReplaceThreadMembersRequest struct {
	MemberIDs []string `json:"member_ids"`
}

type Thread struct {
	ID             string      `json:"id"`
	Kind           ThreadKind  `json:"kind"`
	ClientThreadID string      `json:"client_thread_id,omitempty"`
	Context        *ContextRef `json:"context,omitempty"`
	Title          string      `json:"title,omitempty"`
	MemberIDs      []string    `json:"member_ids"`
	LastMessageSeq int64       `json:"last_message_seq"`
	LastReadSeq    int64       `json:"last_read_seq"`
	UnreadCount    int64       `json:"unread_count"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

// Attachment has a generic transport identity. Capture metadata is optional
// extension data useful to camera clients, not a required business workflow.
type Attachment struct {
	ID                     string         `json:"id"`
	FileID                 string         `json:"file_id"`
	Filename               string         `json:"filename,omitempty"`
	ContentType            string         `json:"content_type"`
	SizeBytes              int64          `json:"size_bytes"`
	SHA256                 string         `json:"sha256,omitempty"`
	TakenAt                string         `json:"taken_at,omitempty"`
	LocationStatus         LocationStatus `json:"location_status,omitempty"`
	Latitude               *float64       `json:"latitude,omitempty"`
	Longitude              *float64       `json:"longitude,omitempty"`
	LocationAccuracyMeters *float64       `json:"location_accuracy_meters,omitempty"`
}

type CreateAttachmentRequest struct {
	FileID                 string         `json:"file_id"`
	Filename               string         `json:"filename,omitempty"`
	ContentType            string         `json:"content_type"`
	SizeBytes              int64          `json:"size_bytes"`
	SHA256                 string         `json:"sha256,omitempty"`
	TakenAt                string         `json:"taken_at,omitempty"`
	LocationStatus         LocationStatus `json:"location_status,omitempty"`
	Latitude               *float64       `json:"latitude,omitempty"`
	Longitude              *float64       `json:"longitude,omitempty"`
	LocationAccuracyMeters *float64       `json:"location_accuracy_meters,omitempty"`
}

// Structured messages are the caller extension point. ContentType names the
// caller-owned contract and Data remains opaque to IM after JSON validation.
type CreateMessageRequest struct {
	ClientMessageID string                    `json:"client_message_id"`
	Kind            MessageKind               `json:"kind"`
	Text            string                    `json:"text,omitempty"`
	TemplateKey     string                    `json:"template_key,omitempty"`
	ContentType     string                    `json:"content_type,omitempty"`
	Data            json.RawMessage           `json:"data,omitempty"`
	Tags            []string                  `json:"tags,omitempty"`
	Attachments     []CreateAttachmentRequest `json:"attachments,omitempty"`
}

type Message struct {
	ID              string          `json:"id"`
	ThreadID        string          `json:"thread_id"`
	Sequence        int64           `json:"sequence"`
	ClientMessageID string          `json:"client_message_id"`
	SenderID        string          `json:"sender_id"`
	SenderName      string          `json:"sender_name"`
	Kind            MessageKind     `json:"kind"`
	Text            string          `json:"text,omitempty"`
	TemplateKey     string          `json:"template_key,omitempty"`
	ContentType     string          `json:"content_type,omitempty"`
	Data            json.RawMessage `json:"data,omitempty"`
	Tags            []string        `json:"tags"`
	Attachments     []Attachment    `json:"attachments"`
	CreatedAt       time.Time       `json:"created_at"`
}

type MessagePage struct {
	Messages     []Message `json:"messages"`
	NextSequence int64     `json:"next_sequence"`
	HasMore      bool      `json:"has_more"`
}

type ContextHistory struct {
	Thread   Thread       `json:"thread"`
	Messages MessagePage  `json:"messages"`
	Receipts []ReadCursor `json:"receipts"`
}

type QuickReply struct {
	Key       string    `json:"key"`
	Text      string    `json:"text"`
	Tags      []string  `json:"tags"`
	SortOrder int       `json:"sort_order"`
	Enabled   bool      `json:"enabled"`
	UpdatedBy string    `json:"updated_by"`
	UpdatedAt time.Time `json:"updated_at"`
}

type PutQuickReplyRequest struct {
	Text      string   `json:"text"`
	Tags      []string `json:"tags,omitempty"`
	SortOrder int      `json:"sort_order"`
	Enabled   bool     `json:"enabled"`
}

type ReadRequest struct {
	Sequence int64 `json:"sequence"`
}

type ReadCursor struct {
	ThreadID  string    `json:"thread_id"`
	SubjectID string    `json:"subject_id"`
	Sequence  int64     `json:"sequence"`
	ReadAt    time.Time `json:"read_at"`
}

type SearchMessagesRequest struct {
	ThreadID string `json:"thread_id,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Query    string `json:"query,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type SyncState struct {
	Cursor      string `json:"cursor"`
	UnreadCount int64  `json:"unread_count"`
}
