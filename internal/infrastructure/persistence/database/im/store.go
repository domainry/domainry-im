package im

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	ormdriver "github.com/domainry/domainry-orm/driver"
	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-orm/sqlhost"
)

type Store struct {
	database *sql.DB
	dialect  Dialect
	profile  ormdriver.Profile
}

func NewStore(database *sql.DB, dialect Dialect, profile ormdriver.Profile) (*Store, error) {
	if database == nil || dialect == nil || profile == nil {
		return nil, fmt.Errorf("IM database, dialect, and driver profile are required")
	}
	return &Store{database: database, dialect: dialect, profile: profile}, nil
}

func (s *Store) Close() error { return s.database.Close() }

func (s *Store) Ready(ctx context.Context) error { return s.database.PingContext(ctx) }

func (s *Store) CreateThread(ctx context.Context, actor model.Actor, thread model.Thread) (model.Thread, error) {
	var out model.Thread
	err := s.write(ctx, func(tx sqlhost.DBTX) error {
		found, err := s.threadExists(ctx, tx, actor.WorkspaceID, thread.ID)
		if err != nil {
			return err
		}
		if found {
			if err := s.requireMembership(ctx, tx, actor, thread.ID); err != nil {
				return err
			}
		} else {
			payload, err := encode(thread)
			if err != nil {
				return err
			}
			contextNamespace, contextResourceType, contextResourceID := "", "", ""
			if thread.Context != nil {
				contextNamespace, contextResourceType, contextResourceID = thread.Context.Namespace, thread.Context.ResourceType, thread.Context.ResourceID
			}
			statement, args, err := query.NewInsertBuilder(s.dialect, threadsTable).Columns("thread_id", "workspace_id", "kind", "client_thread_id", "context_namespace", "context_resource_type", "context_resource_id", "title", "created_by", "last_message_seq", "receipt_revision", "created_at_ms", "updated_at_ms", "payload_json").Values(thread.ID, actor.WorkspaceID, string(thread.Kind), thread.ClientThreadID, contextNamespace, contextResourceType, contextResourceID, thread.Title, actor.SubjectID, int64(0), int64(0), millis(thread.CreatedAt), millis(thread.UpdatedAt), payload).Build()
			if err := exec(ctx, tx, statement, args, err); err != nil {
				return err
			}
			for _, subjectID := range thread.MemberIDs {
				builder := query.NewInsertBuilder(s.dialect, membersTable).Columns("workspace_id", "thread_id", "subject_id", "joined_at_ms", "last_read_seq", "read_at_ms").Values(actor.WorkspaceID, thread.ID, subjectID, millis(thread.CreatedAt), int64(0), int64(0)).OnConflictDoNothing("thread_id", "subject_id")
				statement, args, err := builder.Build()
				if err := exec(ctx, tx, statement, args, err); err != nil {
					return err
				}
			}
		}
		out, err = s.loadThread(ctx, tx, actor, thread.ID)
		return err
	})
	return out, err
}

func (s *Store) ListThreads(ctx context.Context, actor model.Actor, limit int) ([]model.Thread, error) {
	builder := query.NewSelectBuilder(s.dialect, threadsTable).Alias("t").Projections(
		query.Project(query.QualifiedColumn("t", "thread_id")), query.Project(query.QualifiedColumn("t", "payload_json")), query.Project(query.QualifiedColumn("t", "last_message_seq")), query.Project(query.QualifiedColumn("m", "last_read_seq")), query.Project(query.QualifiedColumn("t", "created_at_ms")), query.Project(query.QualifiedColumn("t", "updated_at_ms")),
	).Join(query.InnerJoin(membersTable, "m", query.EqualExpressions(query.QualifiedColumn("m", "thread_id"), query.QualifiedColumn("t", "thread_id")))).Where(query.And(
		query.EqualValue(query.QualifiedColumn("t", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("m", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("m", "subject_id"), actor.SubjectID),
	)).OrderBy(query.DescendingExpression(query.QualifiedColumn("t", "updated_at_ms")), query.AscendingExpression(query.QualifiedColumn("t", "thread_id"))).Limit(limit)
	statement, args, err := builder.Build()
	if err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]model.Thread, 0)
	for rows.Next() {
		var value model.Thread
		var raw []byte
		var lastMessageSeq, lastReadSeq, createdAt, updatedAt int64
		if err := rows.Scan(&value.ID, &raw, &lastMessageSeq, &lastReadSeq, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		value.LastMessageSeq, value.LastReadSeq = lastMessageSeq, lastReadSeq
		value.CreatedAt, value.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
		value.UnreadCount = max(0, value.LastMessageSeq-value.LastReadSeq)
		value.MemberIDs, err = s.listMembers(ctx, s.database, actor.WorkspaceID, value.ID)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) AppendMessage(ctx context.Context, actor model.Actor, threadID string, message model.Message) (model.Message, bool, error) {
	var out model.Message
	created := false
	err := s.write(ctx, func(tx sqlhost.DBTX) error {
		if err := s.requireMembership(ctx, tx, actor, threadID); err != nil {
			return err
		}
		statement, args, err := query.NewSelectBuilder(s.dialect, messagesTable).Columns("message_id").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID), query.Equal("sender_subject_id", actor.SubjectID), query.Equal("client_message_id", message.ClientMessageID))).Build()
		if err != nil {
			return err
		}
		var existingID string
		err = tx.QueryRowContext(ctx, statement, args...).Scan(&existingID)
		if err == nil {
			out, err = s.loadMessage(ctx, tx, actor.WorkspaceID, existingID)
			if err == nil && !sameClientMessage(out, message) {
				return model.ErrConflict
			}
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		update, updateArgs, err := query.NewUpdateBuilder(s.dialect, threadsTable).SetExpression("last_message_seq", query.Add(query.Column("last_message_seq"), query.Value(int64(1)))).Set("updated_at_ms", millis(message.CreatedAt)).Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID))).Build()
		if err := exec(ctx, tx, update, updateArgs, err); err != nil {
			return err
		}
		selectSequence, sequenceArgs, err := query.NewSelectBuilder(s.dialect, threadsTable).Columns("last_message_seq").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID))).Build()
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, selectSequence, sequenceArgs...).Scan(&message.Sequence); err != nil {
			return err
		}
		message.ThreadID = threadID
		if message.Attachments == nil {
			message.Attachments = []model.Attachment{}
		}
		messagePayload, err := encode(message)
		if err != nil {
			return err
		}
		insert, insertArgs, err := query.NewInsertBuilder(s.dialect, messagesTable).Columns("message_id", "workspace_id", "thread_id", "sequence", "client_message_id", "sender_subject_id", "content_type", "body_text", "created_at_ms", "payload_json").Values(message.ID, actor.WorkspaceID, threadID, message.Sequence, message.ClientMessageID, actor.SubjectID, message.ContentType, message.Text, millis(message.CreatedAt), messagePayload).Build()
		if err := exec(ctx, tx, insert, insertArgs, err); err != nil {
			return err
		}
		for _, tag := range message.Tags {
			insert, insertArgs, err = query.NewInsertBuilder(s.dialect, messageTagsTable).Columns("message_id", "workspace_id", "tag", "created_at_ms").Values(message.ID, actor.WorkspaceID, tag, millis(message.CreatedAt)).Build()
			if err := exec(ctx, tx, insert, insertArgs, err); err != nil {
				return err
			}
		}
		for _, attachment := range message.Attachments {
			attachmentPayload, err := encode(attachment)
			if err != nil {
				return err
			}
			insert, insertArgs, err = query.NewInsertBuilder(s.dialect, attachmentsTable).Columns("attachment_id", "workspace_id", "message_id", "file_id", "payload_json").Values(attachment.ID, actor.WorkspaceID, message.ID, attachment.FileID, attachmentPayload).Build()
			if err := exec(ctx, tx, insert, insertArgs, err); err != nil {
				return err
			}
		}
		out, created = message, true
		return nil
	})
	return out, created, err
}

func (s *Store) ListMessages(ctx context.Context, actor model.Actor, threadID string, afterSequence int64, limit int) (model.MessagePage, error) {
	if err := s.requireMembership(ctx, s.database, actor, threadID); err != nil {
		return model.MessagePage{}, err
	}
	return s.listMessages(ctx, s.database, actor.WorkspaceID, threadID, afterSequence, limit)
}

func (s *Store) listMessages(ctx context.Context, db sqlhost.DBTX, workspaceID, threadID string, afterSequence int64, limit int) (model.MessagePage, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, messagesTable).Columns("payload_json").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("thread_id", threadID), query.GreaterThan("sequence", afterSequence))).OrderBy(query.Ascending("sequence")).Limit(limit + 1).Build()
	if err != nil {
		return model.MessagePage{}, err
	}
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return model.MessagePage{}, err
	}
	messages, err := scanJSON[model.Message](rows)
	if err != nil {
		return model.MessagePage{}, err
	}
	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}
	next := afterSequence
	if len(messages) > 0 {
		next = messages[len(messages)-1].Sequence
	}
	return model.MessagePage{Messages: messages, NextSequence: next, HasMore: hasMore}, nil
}

func (s *Store) SearchMessages(ctx context.Context, actor model.Actor, request model.SearchMessagesRequest) ([]model.Message, error) {
	filters := []query.Predicate{
		query.EqualValue(query.QualifiedColumn("m", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("tm", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("tm", "subject_id"), actor.SubjectID),
	}
	if request.ThreadID != "" {
		filters = append(filters, query.EqualValue(query.QualifiedColumn("m", "thread_id"), request.ThreadID))
	}
	if request.Query != "" {
		filters = append(filters, query.LikeValueEscaped(query.QualifiedColumn("m", "body_text"), "%"+escapeLike(request.Query)+"%"))
	}
	joins := []query.Join{query.InnerJoin(membersTable, "tm", query.EqualExpressions(query.QualifiedColumn("tm", "thread_id"), query.QualifiedColumn("m", "thread_id")))}
	if request.Tag != "" {
		joins = append(joins, query.InnerJoin(messageTagsTable, "mt", query.EqualExpressions(query.QualifiedColumn("mt", "message_id"), query.QualifiedColumn("m", "message_id"))))
		filters = append(filters, query.EqualValue(query.QualifiedColumn("mt", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("mt", "tag"), request.Tag))
	}
	builder := query.NewSelectBuilder(s.dialect, messagesTable).Alias("m").Projections(query.Project(query.QualifiedColumn("m", "payload_json"))).Join(joins...)
	builder = builder.Where(query.And(filters...)).OrderBy(query.DescendingExpression(query.QualifiedColumn("m", "created_at_ms")), query.DescendingExpression(query.QualifiedColumn("m", "message_id"))).Limit(request.Limit)
	statement, args, err := builder.Build()
	if err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	return scanJSON[model.Message](rows)
}

func (s *Store) ListReadCursors(ctx context.Context, actor model.Actor, threadID string) ([]model.ReadCursor, error) {
	if err := s.requireMembership(ctx, s.database, actor, threadID); err != nil {
		return nil, err
	}
	return s.listReadCursors(ctx, s.database, actor.WorkspaceID, threadID)
}

func (s *Store) listReadCursors(ctx context.Context, db sqlhost.DBTX, workspaceID, threadID string) ([]model.ReadCursor, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, membersTable).Columns("subject_id", "last_read_seq", "read_at_ms").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("thread_id", threadID))).OrderBy(query.Ascending("subject_id")).Build()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]model.ReadCursor, 0)
	for rows.Next() {
		var value model.ReadCursor
		var readAt int64
		value.ThreadID = threadID
		if err := rows.Scan(&value.SubjectID, &value.Sequence, &readAt); err != nil {
			return nil, err
		}
		value.ReadAt = fromMillis(readAt)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) MarkRead(ctx context.Context, actor model.Actor, threadID string, sequence int64, readAt time.Time) (model.ReadCursor, bool, error) {
	var cursor model.ReadCursor
	changed := false
	err := s.write(ctx, func(tx sqlhost.DBTX) error {
		if err := s.requireMembership(ctx, tx, actor, threadID); err != nil {
			return err
		}
		statement, args, err := query.NewSelectBuilder(s.dialect, threadsTable).Columns("last_message_seq").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID))).Build()
		if err != nil {
			return err
		}
		var last int64
		if err := tx.QueryRowContext(ctx, statement, args...).Scan(&last); err != nil {
			return err
		}
		sequence = min(sequence, last)
		update, updateArgs, err := query.NewUpdateBuilder(s.dialect, membersTable).Set("last_read_seq", sequence).Set("read_at_ms", millis(readAt)).Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID), query.Equal("subject_id", actor.SubjectID), query.LessThan("last_read_seq", sequence))).Build()
		if err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, update, updateArgs...)
		if err != nil {
			return err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return err
		}
		changed = count > 0
		if changed {
			threadUpdate, threadArgs, err := query.NewUpdateBuilder(s.dialect, threadsTable).SetExpression("receipt_revision", query.Add(query.Column("receipt_revision"), query.Value(int64(1)))).Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID))).Build()
			if err := exec(ctx, tx, threadUpdate, threadArgs, err); err != nil {
				return err
			}
		}
		read, readArgs, err := query.NewSelectBuilder(s.dialect, membersTable).Columns("last_read_seq", "read_at_ms").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID), query.Equal("subject_id", actor.SubjectID))).Build()
		if err != nil {
			return err
		}
		var at int64
		if err := tx.QueryRowContext(ctx, read, readArgs...).Scan(&cursor.Sequence, &at); err != nil {
			return err
		}
		cursor.ThreadID, cursor.SubjectID, cursor.ReadAt = threadID, actor.SubjectID, fromMillis(at)
		return nil
	})
	return cursor, changed, err
}

func (s *Store) GetAttachment(ctx context.Context, actor model.Actor, attachmentID string) (model.Attachment, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, attachmentsTable).Columns("message_id", "payload_json").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("attachment_id", attachmentID))).Build()
	if err != nil {
		return model.Attachment{}, err
	}
	var messageID string
	var raw []byte
	err = s.database.QueryRowContext(ctx, statement, args...).Scan(&messageID, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Attachment{}, model.ErrAttachmentNotFound
	}
	if err != nil {
		return model.Attachment{}, err
	}
	message, err := s.loadMessage(ctx, s.database, actor.WorkspaceID, messageID)
	if err != nil {
		return model.Attachment{}, err
	}
	if err := s.requireMembership(ctx, s.database, actor, message.ThreadID); err != nil {
		if errors.Is(err, model.ErrNotMember) {
			return model.Attachment{}, model.ErrAttachmentNotFound
		}
		return model.Attachment{}, err
	}
	var out model.Attachment
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.Attachment{}, err
	}
	return out, nil
}

func (s *Store) SyncState(ctx context.Context, actor model.Actor) (model.SyncState, error) {
	builder := query.NewSelectBuilder(s.dialect, threadsTable).Alias("t").Projections(
		query.Project(query.CountAll()),
		query.Project(query.Sum(query.QualifiedColumn("t", "last_message_seq"))),
		query.Project(query.Sum(query.QualifiedColumn("m", "last_read_seq"))),
		query.Project(query.Sum(query.QualifiedColumn("t", "receipt_revision"))),
		query.Project(query.Sum(query.QualifiedColumn("t", "updated_at_ms"))),
	).Join(query.InnerJoin(membersTable, "m", query.EqualExpressions(query.QualifiedColumn("m", "thread_id"), query.QualifiedColumn("t", "thread_id")))).Where(query.And(query.EqualValue(query.QualifiedColumn("t", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("m", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("m", "subject_id"), actor.SubjectID)))
	statement, args, err := builder.Build()
	if err != nil {
		return model.SyncState{}, err
	}
	var count int64
	var lastTotal, readTotal, receiptTotal, updatedTotal sql.NullInt64
	if err := s.database.QueryRowContext(ctx, statement, args...).Scan(&count, &lastTotal, &readTotal, &receiptTotal, &updatedTotal); err != nil {
		return model.SyncState{}, err
	}
	quickStatement, quickArgs, err := query.NewSelectBuilder(s.dialect, quickRepliesTable).Projections(query.Project(query.CountAll()), query.Project(query.Sum(query.Column("updated_at_ms")))).Where(query.Equal("workspace_id", actor.WorkspaceID)).Build()
	if err != nil {
		return model.SyncState{}, err
	}
	var quickCount int64
	var quickUpdatedTotal sql.NullInt64
	if err := s.database.QueryRowContext(ctx, quickStatement, quickArgs...).Scan(&quickCount, &quickUpdatedTotal); err != nil {
		return model.SyncState{}, err
	}
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00%d\x00", actor.WorkspaceID, actor.SubjectID, count, lastTotal.Int64, readTotal.Int64, receiptTotal.Int64, updatedTotal.Int64, quickCount, quickUpdatedTotal.Int64)
	return model.SyncState{Cursor: hex.EncodeToString(hash.Sum(nil)), UnreadCount: max(0, lastTotal.Int64-readTotal.Int64)}, nil
}

func (s *Store) loadThread(ctx context.Context, db sqlhost.DBTX, actor model.Actor, threadID string) (model.Thread, error) {
	out, err := s.loadThreadRecord(ctx, db, actor.WorkspaceID, threadID)
	if err != nil {
		return model.Thread{}, err
	}
	membership, membershipArgs, err := query.NewSelectBuilder(s.dialect, membersTable).Columns("last_read_seq").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID), query.Equal("subject_id", actor.SubjectID))).Build()
	if err != nil {
		return model.Thread{}, err
	}
	if err := db.QueryRowContext(ctx, membership, membershipArgs...).Scan(&out.LastReadSeq); errors.Is(err, sql.ErrNoRows) {
		return model.Thread{}, model.ErrNotMember
	} else if err != nil {
		return model.Thread{}, err
	}
	out.UnreadCount = max(0, out.LastMessageSeq-out.LastReadSeq)
	return out, nil
}

func (s *Store) loadThreadRecord(ctx context.Context, db sqlhost.DBTX, workspaceID, threadID string) (model.Thread, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, threadsTable).Columns("payload_json", "last_message_seq", "created_at_ms", "updated_at_ms").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("thread_id", threadID))).Build()
	if err != nil {
		return model.Thread{}, err
	}
	var out model.Thread
	var raw []byte
	var lastMessageSeq, createdAt, updatedAt int64
	err = db.QueryRowContext(ctx, statement, args...).Scan(&raw, &lastMessageSeq, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Thread{}, model.ErrThreadNotFound
	}
	if err != nil {
		return model.Thread{}, err
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.Thread{}, err
	}
	out.LastMessageSeq = lastMessageSeq
	out.CreatedAt, out.UpdatedAt = fromMillis(createdAt), fromMillis(updatedAt)
	out.MemberIDs, err = s.listMembers(ctx, db, workspaceID, threadID)
	return out, err
}

func (s *Store) loadMessage(ctx context.Context, db sqlhost.DBTX, workspaceID, messageID string) (model.Message, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, messagesTable).Columns("payload_json").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("message_id", messageID))).Build()
	if err != nil {
		return model.Message{}, err
	}
	var raw []byte
	if err := db.QueryRowContext(ctx, statement, args...).Scan(&raw); err != nil {
		return model.Message{}, err
	}
	var out model.Message
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.Message{}, err
	}
	return out, nil
}

func (s *Store) requireMembership(ctx context.Context, db sqlhost.DBTX, actor model.Actor, threadID string) error {
	statement, args, err := query.NewSelectBuilder(s.dialect, membersTable).Columns("subject_id").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID), query.Equal("subject_id", actor.SubjectID))).Build()
	if err != nil {
		return err
	}
	var found string
	err = db.QueryRowContext(ctx, statement, args...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ErrNotMember
	}
	return err
}

func (s *Store) threadExists(ctx context.Context, db sqlhost.DBTX, workspaceID, threadID string) (bool, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, threadsTable).Columns("thread_id").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("thread_id", threadID))).Build()
	if err != nil {
		return false, err
	}
	var found string
	err = db.QueryRowContext(ctx, statement, args...).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (s *Store) listMembers(ctx context.Context, db sqlhost.DBTX, workspaceID, threadID string) ([]string, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, membersTable).Columns("subject_id").Where(query.And(query.Equal("workspace_id", workspaceID), query.Equal("thread_id", threadID))).OrderBy(query.Ascending("subject_id")).Limit(200).Build()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *Store) write(ctx context.Context, fn func(sqlhost.DBTX) error) error {
	for attempt := 0; ; attempt++ {
		tx, err := s.profile.BeginWrite(ctx, s.database)
		if err == nil {
			err = fn(tx)
			if err == nil {
				err = tx.Commit(ctx)
			}
			_ = tx.Rollback(context.WithoutCancel(ctx))
		}
		if err == nil || attempt >= 4 || !transient(s.profile.ClassifyError(err)) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(5<<attempt) * time.Millisecond):
		}
	}
}

func transient(kind ormdriver.ErrorKind) bool {
	return kind == ormdriver.ErrorSerialization || kind == ormdriver.ErrorDeadlock || kind == ormdriver.ErrorUnavailable || kind == ormdriver.ErrorTimeout
}

func exec(ctx context.Context, db sqlhost.DBTX, statement string, args []any, err error) error {
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, statement, args...)
	return err
}

func scanJSON[T any](rows *sql.Rows) ([]T, error) {
	defer rows.Close()
	values := make([]T, 0)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var value T
		if err := json.Unmarshal(raw, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func encode(value any) ([]byte, error) { return json.Marshal(value) }

func millis(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func fromMillis(value int64) time.Time {
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, "~", "~~")
	value = strings.ReplaceAll(value, "%", "~%")
	return strings.ReplaceAll(value, "_", "~_")
}

func sameClientMessage(existing, candidate model.Message) bool {
	if existing.SenderID != candidate.SenderID || existing.Kind != candidate.Kind || existing.Text != candidate.Text || existing.TemplateKey != candidate.TemplateKey || existing.ContentType != candidate.ContentType || !bytes.Equal(existing.Data, candidate.Data) || !sameStrings(existing.Tags, candidate.Tags) || len(existing.Attachments) != len(candidate.Attachments) {
		return false
	}
	for index := range existing.Attachments {
		left, right := existing.Attachments[index], candidate.Attachments[index]
		if left.FileID != right.FileID || left.Filename != right.Filename || left.ContentType != right.ContentType || left.SizeBytes != right.SizeBytes || left.SHA256 != right.SHA256 || left.TakenAt != right.TakenAt || left.LocationStatus != right.LocationStatus || !sameFloat(left.Latitude, right.Latitude) || !sameFloat(left.Longitude, right.Longitude) || !sameFloat(left.LocationAccuracyMeters, right.LocationAccuracyMeters) {
			return false
		}
	}
	return true
}

func sameFloat(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
