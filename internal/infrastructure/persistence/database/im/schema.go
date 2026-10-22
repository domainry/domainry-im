package im

import (
	"fmt"

	"github.com/domainry/domainry-foundation/schemaownership"
	"github.com/domainry/domainry-orm/migration"
	ormschema "github.com/domainry/domainry-orm/schema"
)

const MigrationOwner = "im"

const (
	threadsTable      = "im_threads"
	membersTable      = "im_thread_members"
	messagesTable     = "im_messages"
	messageTagsTable  = "im_message_tags"
	attachmentsTable  = "im_attachments"
	quickRepliesTable = "im_quick_replies"
)

type Dialect interface {
	Identifier(string) string
	Table(string) string
	Placeholder(int) string
}

var ownership = []schemaownership.Table{
	{Name: threadsTable, Owner: MigrationOwner, Kind: schemaownership.KindEntity, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionProduct, PrimaryKey: []string{"id"}, EntityIDColumn: "thread_id", EntityIDPrefix: "imthr", BusinessKey: []string{"thread_id"}, BoundedQueryPath: "workspace membership or exact opaque context", DeletionPolicy: "workspace deletion removes all conversation threads"},
	{Name: membersTable, Owner: MigrationOwner, Kind: schemaownership.KindRelation, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionUserErase, PrimaryKey: []string{"id"}, BoundedQueryPath: "workspace, subject and thread membership lookup", DeletionPolicy: "subject erasure removes memberships and read cursors"},
	{Name: messagesTable, Owner: MigrationOwner, Kind: schemaownership.KindEntity, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionProduct, PrimaryKey: []string{"id"}, EntityIDColumn: "message_id", EntityIDPrefix: "immsg", BusinessKey: []string{"message_id"}, BoundedQueryPath: "workspace member threads with sequence or bounded search", DeletionPolicy: "workspace deletion removes messages"},
	{Name: messageTagsTable, Owner: MigrationOwner, Kind: schemaownership.KindRelation, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionProduct, PrimaryKey: []string{"id"}, BoundedQueryPath: "workspace and exact tag", DeletionPolicy: "message deletion removes tags"},
	{Name: attachmentsTable, Owner: MigrationOwner, Kind: schemaownership.KindEntity, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionProduct, PrimaryKey: []string{"id"}, EntityIDColumn: "attachment_id", EntityIDPrefix: "imatt", BusinessKey: []string{"attachment_id"}, BoundedQueryPath: "workspace attachment identity after membership authorization", DeletionPolicy: "workspace deletion removes attachment metadata and objects"},
	{Name: quickRepliesTable, Owner: MigrationOwner, Kind: schemaownership.KindDependent, WorkspaceScope: schemaownership.ScopeWorkspace, RetentionClass: schemaownership.RetentionProduct, PrimaryKey: []string{"id"}, BoundedQueryPath: "workspace bounded quick reply list", DeletionPolicy: "workspace deletion removes quick replies"},
}

func SchemaOwnership() []schemaownership.Table { return schemaownership.Clone(ownership) }
func OwnedTables() []string                    { return schemaownership.Names(SchemaOwnership()) }

func SchemaMigrations(d Dialect) ([]migration.Migration, error) {
	if d == nil {
		return nil, fmt.Errorf("IM schema dialect is required")
	}
	m := migration.Migration{Version: 1, Name: "create_im_schema"}
	builders := []interface {
		Build() (string, []any, error)
	}{
		physicalTable(d, threadsTable).IfNotExists().Columns(
			required("thread_id", ormschema.TextKey(96)), required("workspace_id", ormschema.TextKey(255)), required("kind", ormschema.TextKey(32)), required("client_thread_id", ormschema.TextKey(128)), required("context_namespace", ormschema.TextKey(255)), required("context_resource_type", ormschema.TextKey(255)), required("context_resource_id", ormschema.TextKey(255)), required("title", ormschema.Text()), required("created_by", ormschema.TextKey(255)), required("last_message_seq", ormschema.BigInt()), required("receipt_revision", ormschema.BigInt()), required("created_at_ms", ormschema.BigInt()), required("updated_at_ms", ormschema.BigInt()), required("payload_json", ormschema.LongText()),
		).PrimaryKey("id").Unique("thread_id"),
		ormschema.NewIndex(d, "idx_im_threads_workspace_updated", threadsTable).Columns("workspace_id", "updated_at_ms"),
		ormschema.NewIndex(d, "idx_im_threads_context", threadsTable).Columns("workspace_id", "context_namespace", "context_resource_type", "context_resource_id"),
		physicalTable(d, membersTable).IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(255)), required("thread_id", ormschema.TextKey(96)), required("subject_id", ormschema.TextKey(255)), required("joined_at_ms", ormschema.BigInt()), required("last_read_seq", ormschema.BigInt()), required("read_at_ms", ormschema.BigInt()),
		).PrimaryKey("id").Unique("thread_id", "subject_id"),
		ormschema.NewIndex(d, "idx_im_members_subject", membersTable).Columns("workspace_id", "subject_id", "thread_id"),
		physicalTable(d, messagesTable).IfNotExists().Columns(
			required("message_id", ormschema.TextKey(96)), required("workspace_id", ormschema.TextKey(255)), required("thread_id", ormschema.TextKey(96)), required("sequence", ormschema.BigInt()), required("client_message_id", ormschema.TextKey(128)), required("sender_subject_id", ormschema.TextKey(255)), required("content_type", ormschema.TextKey(128)), required("body_text", ormschema.LongText()), required("created_at_ms", ormschema.BigInt()), required("payload_json", ormschema.LongText()),
		).PrimaryKey("id").Unique("message_id").Unique("thread_id", "sequence").Unique("thread_id", "sender_subject_id", "client_message_id"),
		ormschema.NewIndex(d, "idx_im_messages_thread_sequence", messagesTable).Columns("workspace_id", "thread_id", "sequence"),
		ormschema.NewIndex(d, "idx_im_messages_search", messagesTable).Columns("workspace_id", "content_type", "created_at_ms"),
		physicalTable(d, messageTagsTable).IfNotExists().Columns(
			required("message_id", ormschema.TextKey(96)), required("workspace_id", ormschema.TextKey(255)), required("tag", ormschema.TextKey(64)), required("created_at_ms", ormschema.BigInt()),
		).PrimaryKey("id").Unique("message_id", "tag"),
		ormschema.NewIndex(d, "idx_im_message_tags_lookup", messageTagsTable).Columns("workspace_id", "tag", "created_at_ms", "message_id"),
		physicalTable(d, attachmentsTable).IfNotExists().Columns(
			required("attachment_id", ormschema.TextKey(96)), required("workspace_id", ormschema.TextKey(255)), required("message_id", ormschema.TextKey(96)), required("file_id", ormschema.TextKey(96)), required("payload_json", ormschema.LongText()),
		).PrimaryKey("id").Unique("attachment_id"),
		ormschema.NewIndex(d, "idx_im_attachments_message", attachmentsTable).Columns("workspace_id", "message_id"),
		physicalTable(d, quickRepliesTable).IfNotExists().Columns(
			required("workspace_id", ormschema.TextKey(255)), required("reply_key", ormschema.TextKey(64)), required("enabled", ormschema.Boolean()), required("sort_order", ormschema.Integer()), required("updated_at_ms", ormschema.BigInt()), required("payload_json", ormschema.LongText()),
		).PrimaryKey("id").Unique("workspace_id", "reply_key"),
		ormschema.NewIndex(d, "idx_im_quick_replies_list", quickRepliesTable).Columns("workspace_id", "enabled", "sort_order", "reply_key"),
	}
	for _, builder := range builders {
		statement, args, err := builder.Build()
		if err != nil {
			return nil, err
		}
		if len(args) != 0 {
			return nil, fmt.Errorf("IM DDL unexpectedly contains bind arguments")
		}
		m.Statements = append(m.Statements, statement)
	}
	return []migration.Migration{m}, nil
}

func required(name string, kind ormschema.ColumnType) ormschema.ColumnDefinition {
	return ormschema.Column(name, kind).NotNull()
}

func physicalTable(d Dialect, name string) *ormschema.TableBuilder {
	return ormschema.NewTableWithRequiredColumns(d, name, ormschema.Column("id", ormschema.BigInt()).NotNull().AutoIncrement())
}
