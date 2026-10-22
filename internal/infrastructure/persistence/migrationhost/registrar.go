package migrationhost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/domainry/domainry-orm/migration"
	"github.com/domainry/domainry-orm/query"
	"github.com/domainry/domainry-orm/schema"
)

type Dialect interface {
	Identifier(string) string
	Table(string) string
	Placeholder(int) string
}

type Registrar struct {
	DB       *sql.DB
	Renderer Dialect
}

func (r *Registrar) Prepare(ctx context.Context) error {
	if r == nil || r.DB == nil || r.Renderer == nil {
		return fmt.Errorf("IM migration host is incomplete")
	}
	statement, args, err := schema.NewTableWithRequiredColumns(r.Renderer, "_schema_migrations", schema.Column("id", schema.BigInt()).NotNull().AutoIncrement()).IfNotExists().Columns(
		schema.Column("owner", schema.TextKey(191)).NotNull(), schema.Column("version", schema.BigInt()).NotNull(), schema.Column("name", schema.TextKey(191)).NotNull(), schema.Column("checksum", schema.TextKey(64)).NotNull(), schema.Column("dirty", schema.Boolean()).NotNull(), schema.Column("applied_at", schema.BigInt()).NotNull(),
	).PrimaryKey("id").Unique("owner", "version").Build()
	if err != nil {
		return err
	}
	_, err = r.DB.ExecContext(ctx, statement, args...)
	return err
}

func (r *Registrar) ApplyOwnedMigrations(ctx context.Context, owner string, values []migration.Migration) error {
	for _, value := range values {
		if err := r.apply(ctx, owner, value, migration.Checksum(value)); err != nil {
			return err
		}
	}
	return nil
}

func (r *Registrar) apply(ctx context.Context, owner string, value migration.Migration, checksum string) error {
	if strings.TrimSpace(owner) == "" || value.Version == 0 || strings.TrimSpace(value.Name) == "" {
		return fmt.Errorf("invalid IM migration")
	}
	predicate := query.And(query.Equal("owner", owner), query.Equal("version", value.Version))
	statement, args, err := query.NewSelectBuilder(r.Renderer, "_schema_migrations").Columns("name", "checksum", "dirty").Where(predicate).Build()
	if err != nil {
		return err
	}
	var name, previous string
	var dirty bool
	err = r.DB.QueryRowContext(ctx, statement, args...).Scan(&name, &previous, &dirty)
	if err == nil {
		if dirty {
			return fmt.Errorf("migration %s/%d is dirty", owner, value.Version)
		}
		if name != value.Name || previous != checksum {
			return fmt.Errorf("migration %s/%d checksum drift", owner, value.Version)
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	insert, insertArgs, err := query.NewInsertBuilder(r.Renderer, "_schema_migrations").Columns("owner", "version", "name", "checksum", "dirty", "applied_at").Values(owner, value.Version, value.Name, checksum, true, int64(0)).Build()
	if err != nil {
		return err
	}
	if _, err = r.DB.ExecContext(ctx, insert, insertArgs...); err != nil {
		return err
	}
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, statement := range value.Statements {
		if _, err = tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply %s/%d: %w", owner, value.Version, err)
		}
	}
	complete, completeArgs, err := query.NewUpdateBuilder(r.Renderer, "_schema_migrations").Set("dirty", false).Set("applied_at", time.Now().UTC().UnixMilli()).Where(predicate).Build()
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, complete, completeArgs...); err != nil {
		return err
	}
	return tx.Commit()
}
