package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress               string
	DatabaseDriver              string
	DatabasePath                string
	DatabaseDSN                 string
	DatabaseSchema              string
	DatabaseMaxOpenConnections  int
	DatabaseMaxIdleConnections  int
	DatabaseConnectionLifetime  time.Duration
	DatabaseConnectionIdleTime  time.Duration
	SQLiteBusyTimeout           time.Duration
	SQLiteMaxOpenConnections    int
	SSEPollInterval             time.Duration
	SSEHeartbeat                time.Duration
	IdentityEndpoint            string
	IdentityIssuer              string
	IdentityWorkspaceID         string
	IdentityApplicationKey      string
	IdentityServiceAccessToken  string
	KnowledgeEndpoint           string
	KnowledgeRuntimeID          string
	KnowledgeServiceAccessToken string
}

func FromEnvironment() (Config, error) {
	driver := strings.ToLower(defaultValue("DOMAINRY_IM_DATABASE_DRIVER", "sqlite"))
	busyTimeout, err := durationValue("DOMAINRY_IM_SQLITE_BUSY_TIMEOUT", 5*time.Second)
	if err != nil {
		return Config{}, err
	}
	poll, err := durationValue("DOMAINRY_IM_SSE_POLL_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	heartbeat, err := durationValue("DOMAINRY_IM_SSE_HEARTBEAT", 15*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxOpen, err := integerValue("DOMAINRY_IM_SQLITE_MAX_OPEN_CONNECTIONS", 8)
	if err != nil {
		return Config{}, err
	}
	databaseMaxOpen, err := integerValue("DOMAINRY_IM_DATABASE_MAX_OPEN_CONNECTIONS", 16)
	if err != nil {
		return Config{}, err
	}
	databaseMaxIdle, err := integerValue("DOMAINRY_IM_DATABASE_MAX_IDLE_CONNECTIONS", 8)
	if err != nil {
		return Config{}, err
	}
	databaseLifetime, err := durationValue("DOMAINRY_IM_DATABASE_CONNECTION_LIFETIME", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	databaseIdleTime, err := durationValue("DOMAINRY_IM_DATABASE_CONNECTION_IDLE_TIME", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	applicationKey := defaultValue("DOMAINRY_IM_APPLICATION_KEY", "domainry-im")
	settings := Config{
		ListenAddress:               defaultValue("DOMAINRY_IM_LISTEN_ADDRESS", ":8092"),
		DatabaseDriver:              driver,
		DatabasePath:                defaultValue("DOMAINRY_IM_DATABASE_PATH", "./data/domainry-im.db"),
		DatabaseDSN:                 strings.TrimSpace(os.Getenv("DOMAINRY_IM_DATABASE_DSN")),
		DatabaseSchema:              defaultValue("DOMAINRY_IM_DATABASE_SCHEMA", "public"),
		DatabaseMaxOpenConnections:  databaseMaxOpen,
		DatabaseMaxIdleConnections:  databaseMaxIdle,
		DatabaseConnectionLifetime:  databaseLifetime,
		DatabaseConnectionIdleTime:  databaseIdleTime,
		SQLiteBusyTimeout:           busyTimeout,
		SQLiteMaxOpenConnections:    maxOpen,
		SSEPollInterval:             poll,
		SSEHeartbeat:                heartbeat,
		IdentityEndpoint:            strings.TrimSpace(os.Getenv("DOMAINRY_IM_IDENTITY_ENDPOINT")),
		IdentityIssuer:              strings.TrimSpace(os.Getenv("DOMAINRY_IM_IDENTITY_ISSUER")),
		IdentityWorkspaceID:         strings.TrimSpace(os.Getenv("DOMAINRY_IM_WORKSPACE_ID")),
		IdentityApplicationKey:      applicationKey,
		IdentityServiceAccessToken:  strings.TrimSpace(os.Getenv("DOMAINRY_IM_IDENTITY_SERVICE_TOKEN")),
		KnowledgeEndpoint:           strings.TrimSpace(os.Getenv("DOMAINRY_IM_KNOWLEDGE_ENDPOINT")),
		KnowledgeRuntimeID:          defaultValue("DOMAINRY_IM_KNOWLEDGE_RUNTIME_ID", applicationKey),
		KnowledgeServiceAccessToken: strings.TrimSpace(os.Getenv("DOMAINRY_IM_KNOWLEDGE_SERVICE_TOKEN")),
	}
	if settings.IdentityEndpoint == "" || settings.IdentityIssuer == "" || settings.IdentityWorkspaceID == "" {
		return Config{}, errors.New("Domainry Identity endpoint, issuer, and workspace ID are required")
	}
	if settings.KnowledgeEndpoint == "" || settings.KnowledgeRuntimeID == "" || settings.KnowledgeServiceAccessToken == "" {
		return Config{}, errors.New("Domainry Knowledge endpoint, Runtime ID, and service token are required")
	}
	if settings.DatabaseDriver != "sqlite" && settings.DatabaseDriver != "postgres" {
		return Config{}, errors.New("DOMAINRY_IM_DATABASE_DRIVER must be sqlite or postgres")
	}
	if settings.DatabaseDriver == "sqlite" && (settings.DatabasePath == "" || settings.SQLiteBusyTimeout <= 0 || settings.SQLiteMaxOpenConnections < 1) {
		return Config{}, errors.New("valid SQLite settings are required")
	}
	if settings.DatabaseDriver == "postgres" && settings.DatabaseDSN == "" {
		return Config{}, errors.New("DOMAINRY_IM_DATABASE_DSN is required for PostgreSQL")
	}
	if settings.DatabaseSchema == "" || settings.DatabaseMaxOpenConnections < 1 || settings.DatabaseMaxIdleConnections < 0 || settings.DatabaseMaxIdleConnections > settings.DatabaseMaxOpenConnections || settings.DatabaseConnectionLifetime <= 0 || settings.DatabaseConnectionIdleTime <= 0 {
		return Config{}, errors.New("valid database pool settings are required")
	}
	if settings.SSEPollInterval < 250*time.Millisecond || settings.SSEPollInterval > time.Minute || settings.SSEHeartbeat < 5*time.Second || settings.SSEHeartbeat > time.Minute {
		return Config{}, errors.New("valid SSE settings are required")
	}
	return settings, nil
}

func defaultValue(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func durationValue(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration: %w", name, err)
	}
	return parsed, nil
}

func integerValue(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer: %w", name, err)
	}
	return parsed, nil
}
