package saas

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/domainry/domainry-im/internal/infrastructure/config"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	ormdriver "github.com/domainry/domainry-orm/driver"
	ormpostgres "github.com/domainry/domainry-orm/postgres"
	ormsqlite "github.com/domainry/domainry-orm/sqlite"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

type databaseResources struct {
	database *sql.DB
	renderer ormdialect.Renderer
	profile  ormdriver.Profile
	driver   string
	schema   string
}

func openDatabase(ctx context.Context, settings config.Config) (_ databaseResources, resultErr error) {
	switch settings.DatabaseDriver {
	case "sqlite":
		if err := ensureParent(settings.DatabasePath); err != nil {
			return databaseResources{}, err
		}
		connectionConfig := ormsqlite.DefaultOwnedConnectionConfig(settings.DatabasePath)
		connectionConfig.BusyTimeout = settings.SQLiteBusyTimeout
		connectionConfig.MaxOpenConnections = settings.SQLiteMaxOpenConnections
		connectionConfig.MaxIdleConnections = settings.SQLiteMaxOpenConnections
		dsn, err := connectionConfig.DSN()
		if err != nil {
			return databaseResources{}, err
		}
		database, err := sql.Open("sqlite", dsn)
		if err != nil {
			return databaseResources{}, err
		}
		defer func() {
			if resultErr != nil {
				_ = database.Close()
			}
		}()
		if err := ormsqlite.InitializeOwned(ctx, database, connectionConfig); err != nil {
			return databaseResources{}, err
		}
		dialectValue, err := ormdialect.New(ormdialect.SQLite)
		if err != nil {
			return databaseResources{}, err
		}
		return databaseResources{database: database, renderer: dialectValue.WithSchema(""), profile: ormsqlite.NewProfile(), driver: "sqlite"}, nil
	case "postgres":
		if !ormdialect.ValidIdentifier(settings.DatabaseSchema) {
			return databaseResources{}, fmt.Errorf("DOMAINRY_IM_DATABASE_SCHEMA must be a safe SQL identifier")
		}
		connectionConfig, err := pgx.ParseConfig(settings.DatabaseDSN)
		if err != nil {
			return databaseResources{}, fmt.Errorf("DOMAINRY_IM_DATABASE_DSN is invalid: %w", err)
		}
		if connectionConfig.RuntimeParams == nil {
			connectionConfig.RuntimeParams = make(map[string]string)
		}
		dialectValue, err := ormdialect.New(ormdialect.Postgres)
		if err != nil {
			return databaseResources{}, err
		}
		connectionConfig.RuntimeParams["search_path"] = dialectValue.Identifier(settings.DatabaseSchema)
		database := stdlib.OpenDB(*connectionConfig)
		database.SetMaxOpenConns(settings.DatabaseMaxOpenConnections)
		database.SetMaxIdleConns(settings.DatabaseMaxIdleConnections)
		database.SetConnMaxLifetime(settings.DatabaseConnectionLifetime)
		database.SetConnMaxIdleTime(settings.DatabaseConnectionIdleTime)
		defer func() {
			if resultErr != nil {
				_ = database.Close()
			}
		}()
		if err := database.PingContext(ctx); err != nil {
			return databaseResources{}, fmt.Errorf("connect PostgreSQL: %w", err)
		}
		if _, err := database.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+dialectValue.Identifier(settings.DatabaseSchema)); err != nil {
			return databaseResources{}, fmt.Errorf("prepare PostgreSQL schema: %w", err)
		}
		return databaseResources{database: database, renderer: dialectValue.WithSchema(settings.DatabaseSchema), profile: ormpostgres.NewProfile(), driver: "postgres", schema: settings.DatabaseSchema}, nil
	default:
		return databaseResources{}, fmt.Errorf("unsupported IM database driver %q", settings.DatabaseDriver)
	}
}

func (r databaseResources) applyMigrations(ctx context.Context, apply func() error) error {
	release, err := r.acquireMigrationLock(ctx)
	if err != nil {
		return err
	}
	applyErr := apply()
	releaseContext, cancelRelease := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelRelease()
	releaseErr := release(releaseContext)
	if applyErr != nil {
		return applyErr
	}
	if releaseErr != nil {
		return releaseErr
	}
	return nil
}

func (r databaseResources) acquireMigrationLock(ctx context.Context) (func(context.Context) error, error) {
	if r.driver != "postgres" {
		return func(context.Context) error { return nil }, nil
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return nil, err
	}
	key := "domainry-im:migrations:" + r.schema
	for {
		var acquired bool
		if err := connection.QueryRowContext(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", key).Scan(&acquired); err != nil {
			_ = connection.Close()
			return nil, fmt.Errorf("acquire PostgreSQL migration lock: %w", err)
		}
		if acquired {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = connection.Close()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return func(releaseContext context.Context) error {
		defer connection.Close()
		var released bool
		if err := connection.QueryRowContext(releaseContext, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", key).Scan(&released); err != nil {
			return fmt.Errorf("release PostgreSQL migration lock: %w", err)
		}
		if !released {
			return fmt.Errorf("PostgreSQL migration lock was lost")
		}
		return nil
	}, nil
}
