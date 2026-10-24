package config

import "testing"

func requiredEnvironment(t *testing.T) {
	t.Helper()
	t.Setenv("DOMAINRY_IM_IDENTITY_ENDPOINT", "https://identity.example.test")
	t.Setenv("DOMAINRY_IM_IDENTITY_ISSUER", "https://issuer.example.test")
	t.Setenv("DOMAINRY_IM_WORKSPACE_ID", "workspace-1")
	t.Setenv("DOMAINRY_IM_KNOWLEDGE_ENDPOINT", "https://knowledge.example.test")
	t.Setenv("DOMAINRY_IM_KNOWLEDGE_SERVICE_TOKEN", "service-token")
}

func TestFromEnvironmentDefaultsToSQLiteForLocalDevelopment(t *testing.T) {
	requiredEnvironment(t)
	t.Setenv("DOMAINRY_IM_DATABASE_DRIVER", "")
	t.Setenv("DOMAINRY_IM_DATABASE_DSN", "")
	settings, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DatabaseDriver != "sqlite" || settings.DatabasePath == "" || settings.SQLiteMaxOpenConnections != 8 {
		t.Fatalf("settings=%#v", settings)
	}
}

func TestFromEnvironmentAcceptsPostgreSQLProductionSettings(t *testing.T) {
	requiredEnvironment(t)
	t.Setenv("DOMAINRY_IM_DATABASE_DRIVER", "postgres")
	t.Setenv("DOMAINRY_IM_DATABASE_DSN", "postgres://im:secret@db.example.test:5432/im?sslmode=require")
	t.Setenv("DOMAINRY_IM_DATABASE_SCHEMA", "domainry_im")
	t.Setenv("DOMAINRY_IM_DATABASE_MAX_OPEN_CONNECTIONS", "24")
	t.Setenv("DOMAINRY_IM_DATABASE_MAX_IDLE_CONNECTIONS", "12")
	settings, err := FromEnvironment()
	if err != nil {
		t.Fatal(err)
	}
	if settings.DatabaseDriver != "postgres" || settings.DatabaseSchema != "domainry_im" || settings.DatabaseMaxOpenConnections != 24 || settings.DatabaseMaxIdleConnections != 12 {
		t.Fatalf("settings=%#v", settings)
	}
}

func TestFromEnvironmentRejectsIncompletePostgreSQLAndInvalidPool(t *testing.T) {
	requiredEnvironment(t)
	t.Setenv("DOMAINRY_IM_DATABASE_DRIVER", "postgres")
	t.Setenv("DOMAINRY_IM_DATABASE_DSN", "")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("PostgreSQL without DSN was accepted")
	}
	t.Setenv("DOMAINRY_IM_DATABASE_DSN", "postgres://im:secret@db.example.test:5432/im")
	t.Setenv("DOMAINRY_IM_DATABASE_MAX_OPEN_CONNECTIONS", "4")
	t.Setenv("DOMAINRY_IM_DATABASE_MAX_IDLE_CONNECTIONS", "5")
	if _, err := FromEnvironment(); err == nil {
		t.Fatal("invalid pool bounds were accepted")
	}
}
