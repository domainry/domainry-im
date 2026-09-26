// Package module exposes IM's build-time declarations. Runtime implementation
// remains internal to the independently deployed service.
package module

import (
	actioncontract "github.com/domainry/domainry-foundation/action"
	"github.com/domainry/domainry-foundation/schemaownership"
	imapplication "github.com/domainry/domainry-im/internal/application/im"
	imstore "github.com/domainry/domainry-im/internal/infrastructure/persistence/database/im"
	ormdialect "github.com/domainry/domainry-orm/dialect"
	"github.com/domainry/domainry-orm/migration"
)

func AuthorizationActions() ([]actioncontract.ActionDefinition, error) {
	return imapplication.AuthorizationActions()
}

func SchemaMigrations(driver, schema string) ([]migration.Migration, error) {
	dialectValue, err := ormdialect.Parse(driver)
	if err != nil {
		return nil, err
	}
	return imstore.SchemaMigrations(dialectValue.WithSchema(schema))
}

func SchemaOwnership() []schemaownership.Table { return imstore.SchemaOwnership() }
func OwnedTables() []string                    { return imstore.OwnedTables() }
