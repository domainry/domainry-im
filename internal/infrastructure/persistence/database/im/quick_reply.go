package im

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/domainry/domainry-im/internal/domain/conversation/model"
	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-orm/sqlhost"
)

func (s *Store) ListQuickReplies(ctx context.Context, actor model.Actor, tag string) ([]model.QuickReply, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, quickRepliesTable).Columns("payload_json").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("enabled", true))).OrderBy(query.Ascending("sort_order"), query.Ascending("reply_key")).Limit(200).Build()
	if err != nil {
		return nil, err
	}
	rows, err := s.database.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	values, err := scanJSON[model.QuickReply](rows)
	if err != nil || tag == "" {
		return values, err
	}
	filtered := make([]model.QuickReply, 0, len(values))
	for _, value := range values {
		for _, candidate := range value.Tags {
			if candidate == tag {
				filtered = append(filtered, value)
				break
			}
		}
	}
	return filtered, nil
}

func (s *Store) GetQuickReply(ctx context.Context, actor model.Actor, key string) (model.QuickReply, error) {
	statement, args, err := query.NewSelectBuilder(s.dialect, quickRepliesTable).Columns("payload_json").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("reply_key", key), query.Equal("enabled", true))).Build()
	if err != nil {
		return model.QuickReply{}, err
	}
	var raw []byte
	err = s.database.QueryRowContext(ctx, statement, args...).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.QuickReply{}, model.ErrQuickReplyNotFound
	}
	if err != nil {
		return model.QuickReply{}, err
	}
	var out model.QuickReply
	if err := json.Unmarshal(raw, &out); err != nil {
		return model.QuickReply{}, err
	}
	return out, nil
}

func (s *Store) PutQuickReply(ctx context.Context, actor model.Actor, value model.QuickReply) (model.QuickReply, error) {
	payload, err := encode(value)
	if err != nil {
		return model.QuickReply{}, err
	}
	out := value
	err = s.write(ctx, func(tx sqlhost.DBTX) error {
		lookup, lookupArgs, lookupErr := query.NewSelectBuilder(s.dialect, quickRepliesTable).Columns("payload_json").Where(query.And(query.Equal("workspace_id", actor.WorkspaceID), query.Equal("reply_key", value.Key))).Build()
		if lookupErr != nil {
			return lookupErr
		}
		var existingRaw []byte
		lookupErr = tx.QueryRowContext(ctx, lookup, lookupArgs...).Scan(&existingRaw)
		if lookupErr == nil {
			var existing model.QuickReply
			if err := json.Unmarshal(existingRaw, &existing); err != nil {
				return err
			}
			if sameQuickReply(existing, value) {
				out = existing
				return nil
			}
		} else if !errors.Is(lookupErr, sql.ErrNoRows) {
			return lookupErr
		}
		statement, args, buildErr := query.NewInsertBuilder(s.dialect, quickRepliesTable).
			Columns("workspace_id", "reply_key", "enabled", "sort_order", "updated_at_ms", "payload_json").
			Values(actor.WorkspaceID, value.Key, value.Enabled, value.SortOrder, millis(value.UpdatedAt), payload).
			OnConflictDoUpdate([]string{"workspace_id", "reply_key"},
				query.AssignExpression("enabled", query.InsertedValue("enabled")),
				query.AssignExpression("sort_order", query.InsertedValue("sort_order")),
				query.AssignExpression("updated_at_ms", query.InsertedValue("updated_at_ms")),
				query.AssignExpression("payload_json", query.InsertedValue("payload_json")),
			).Build()
		return exec(ctx, tx, statement, args, buildErr)
	})
	if err != nil {
		return model.QuickReply{}, err
	}
	return out, nil
}

func sameQuickReply(left, right model.QuickReply) bool {
	return left.Key == right.Key && left.Text == right.Text && sameStrings(left.Tags, right.Tags) && left.SortOrder == right.SortOrder && left.Enabled == right.Enabled
}
