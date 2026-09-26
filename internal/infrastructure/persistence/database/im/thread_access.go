package im

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-orm/sqlhost"
)

func (s *Store) ReplaceThreadMembers(ctx context.Context, actor model.Actor, threadID string, memberIDs []string, changedAt time.Time) (model.Thread, error) {
	var out model.Thread
	err := s.write(ctx, func(tx sqlhost.DBTX) error {
		if err := s.requireMembership(ctx, tx, actor, threadID); err != nil {
			return err
		}
		thread, err := s.loadThreadRecord(ctx, tx, actor.WorkspaceID, threadID)
		if err != nil {
			return err
		}
		if thread.Kind == model.ThreadDirect {
			return model.ErrConflict
		}
		if sameStrings(thread.MemberIDs, memberIDs) {
			out = thread
			return nil
		}
		deleteStatement, deleteArgs, err := query.NewDeleteBuilder(s.dialect, membersTable).Where(query.And(
			query.Equal("workspace_id", actor.WorkspaceID),
			query.Equal("thread_id", threadID),
			query.NotIn("subject_id", stringValues(memberIDs)...),
		)).Build()
		if err := exec(ctx, tx, deleteStatement, deleteArgs, err); err != nil {
			return err
		}
		for _, subjectID := range memberIDs {
			statement, args, err := query.NewInsertBuilder(s.dialect, membersTable).
				Columns("workspace_id", "thread_id", "subject_id", "joined_at_ms", "last_read_seq", "read_at_ms").
				Values(actor.WorkspaceID, threadID, subjectID, millis(changedAt), int64(0), int64(0)).
				OnConflictDoNothing("thread_id", "subject_id").Build()
			if err := exec(ctx, tx, statement, args, err); err != nil {
				return err
			}
		}
		statement, args, err := query.NewUpdateBuilder(s.dialect, threadsTable).
			SetExpression("receipt_revision", query.Add(query.Column("receipt_revision"), query.Value(int64(1)))).
			Set("updated_at_ms", millis(changedAt)).
			Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("thread_id", threadID))).Build()
		if err := exec(ctx, tx, statement, args, err); err != nil {
			return err
		}
		out, err = s.loadThreadRecord(ctx, tx, actor.WorkspaceID, threadID)
		return err
	})
	return out, err
}

func (s *Store) ContextHistory(ctx context.Context, actor model.Actor, contextRef model.ContextRef, afterSequence int64, limit int) (model.ContextHistory, error) {
	threadID, err := s.contextThreadID(ctx, actor.WorkspaceID, contextRef)
	if err != nil {
		return model.ContextHistory{}, err
	}
	thread, err := s.loadThreadRecord(ctx, s.database, actor.WorkspaceID, threadID)
	if err != nil {
		return model.ContextHistory{}, err
	}
	messages, err := s.listMessages(ctx, s.database, actor.WorkspaceID, threadID, afterSequence, limit)
	if err != nil {
		return model.ContextHistory{}, err
	}
	receipts, err := s.listReadCursors(ctx, s.database, actor.WorkspaceID, threadID)
	if err != nil {
		return model.ContextHistory{}, err
	}
	return model.ContextHistory{Thread: thread, Messages: messages, Receipts: receipts}, nil
}

func (s *Store) SearchContextHistory(ctx context.Context, actor model.Actor, contextRef model.ContextRef, request model.SearchMessagesRequest) ([]model.Message, error) {
	threadID, err := s.contextThreadID(ctx, actor.WorkspaceID, contextRef)
	if err != nil {
		return nil, err
	}
	predicates := []query.Predicate{query.EqualValue(query.QualifiedColumn("m", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("m", "thread_id"), threadID)}
	if request.Query != "" {
		predicates = append(predicates, query.LikeValueEscaped(query.QualifiedColumn("m", "body_text"), "%"+escapeLike(request.Query)+"%"))
	}
	joins := make([]query.Join, 0, 1)
	if request.Tag != "" {
		joins = append(joins, query.InnerJoin(messageTagsTable, "mt", query.EqualExpressions(query.QualifiedColumn("mt", "message_id"), query.QualifiedColumn("m", "message_id"))))
		predicates = append(predicates, query.EqualValue(query.QualifiedColumn("mt", "workspace_id"), actor.WorkspaceID), query.EqualValue(query.QualifiedColumn("mt", "tag"), request.Tag))
	}
	builder := query.NewSelectBuilder(s.dialect, messagesTable).Alias("m").Projections(query.Project(query.QualifiedColumn("m", "payload_json"))).Join(joins...)
	statement, args, err := builder.Where(query.And(predicates...)).OrderBy(query.DescendingExpression(query.QualifiedColumn("m", "created_at_ms")), query.DescendingExpression(query.QualifiedColumn("m", "message_id"))).Limit(request.Limit).Build()
	if err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	return scanJSON[model.Message](rows)
}

func (s *Store) GetContextAttachment(ctx context.Context, actor model.Actor, attachmentID string) (model.Attachment, error) {
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
	thread, err := s.loadThreadRecord(ctx, s.database, actor.WorkspaceID, message.ThreadID)
	if err != nil || thread.Context == nil {
		return model.Attachment{}, model.ErrAttachmentNotFound
	}
	var out model.Attachment
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.Attachment{}, err
	}
	return out, nil
}

func (s *Store) contextThreadID(ctx context.Context, workspaceID string, contextRef model.ContextRef) (string, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, threadsTable).Columns("thread_id").Where(query.And(
		query.Equal("workspace_id", workspaceID),
		query.Equal("context_namespace", contextRef.Namespace),
		query.Equal("context_resource_type", contextRef.ResourceType),
		query.Equal("context_resource_id", contextRef.ResourceID),
	)).Limit(1).Build()
	if err != nil {
		return "", err
	}
	var threadID string
	err = s.database.QueryRowContext(ctx, statement, args...).Scan(&threadID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", model.ErrThreadNotFound
	}
	return threadID, err
}

func stringValues(values []string) []any {
	result := make([]any, len(values))
	for index, value := range values {
		result[index] = value
	}
	return result
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
