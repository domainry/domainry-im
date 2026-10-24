package saas

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/domainry/domainry-im/internal/infrastructure/config"
)

func TestOpenPostgreSQLRejectsUnsafeSchemaBeforeConnecting(t *testing.T) {
	_, err := openDatabase(t.Context(), config.Config{DatabaseDriver: "postgres", DatabaseDSN: "postgres://localhost/im", DatabaseSchema: "public;drop schema public", DatabaseMaxOpenConnections: 1, DatabaseConnectionLifetime: 1, DatabaseConnectionIdleTime: 1})
	if err == nil || !strings.Contains(err.Error(), "safe SQL identifier") {
		t.Fatalf("err=%v", err)
	}
}

func TestOpenPostgreSQLCreatesSchemaAndUsesMigrationLock(t *testing.T) {
	dsn := os.Getenv("DOMAINRY_IM_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DOMAINRY_IM_TEST_POSTGRES_DSN is not set")
	}
	schema := fmt.Sprintf("domainry_im_assembly_test_%d", time.Now().UTC().UnixNano())
	resources, err := openDatabase(t.Context(), config.Config{
		DatabaseDriver:             "postgres",
		DatabaseDSN:                dsn,
		DatabaseSchema:             schema,
		DatabaseMaxOpenConnections: 4,
		DatabaseMaxIdleConnections: 2,
		DatabaseConnectionLifetime: time.Minute,
		DatabaseConnectionIdleTime: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = resources.database.ExecContext(cleanupContext, "DROP SCHEMA IF EXISTS "+resources.renderer.Identifier(schema)+" CASCADE")
		_ = resources.database.Close()
	})

	for attempt := 0; attempt < 2; attempt++ {
		if err := resources.applyMigrations(t.Context(), func() error {
			_, execErr := resources.database.ExecContext(t.Context(), "CREATE TABLE IF NOT EXISTS "+resources.renderer.Table("migration_probe")+" (id BIGINT PRIMARY KEY)")
			return execErr
		}); err != nil {
			t.Fatalf("migration lock attempt %d: %v", attempt+1, err)
		}
	}
	var tableName string
	if err := resources.database.QueryRowContext(t.Context(), "SELECT table_name FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2", schema, "migration_probe").Scan(&tableName); err != nil {
		t.Fatal(err)
	}
	if tableName != "migration_probe" {
		t.Fatalf("table=%q", tableName)
	}
}
