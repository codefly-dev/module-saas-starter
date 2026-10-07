package bigquerystore

import (
	"testing"
	"time"

	"cloud.google.com/go/bigquery"
	"github.com/stretchr/testify/require"
)

func TestEnsureRejectsColumnsTheWriterCannotSupply(t *testing.T) {
	store := &Store{retention: 30 * 24 * time.Hour}
	for _, spec := range store.tableSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			for _, field := range spec.schema {
				if field.Required {
					continue
				}
				t.Run(field.Name, func(t *testing.T) {
					meta := metadataFor(spec)
					meta.Schema = append(bigquery.Schema(nil), meta.Schema...)
					for i, column := range meta.Schema {
						if column.Name == field.Name {
							copy := *column
							copy.Required = true
							meta.Schema[i] = &copy
							break
						}
					}
					require.ErrorContains(t, conforms(spec, meta), field.Name)
				})
			}
			t.Run("extra required column", func(t *testing.T) {
				meta := metadataFor(spec)
				meta.Schema = append(meta.Schema, &bigquery.FieldSchema{Name: "customer_tag", Type: bigquery.StringFieldType, Required: true})
				require.ErrorContains(t, conforms(spec, meta), "customer_tag")
			})
		})
	}
}

func TestEnsureAcceptsCompatibleSchemaExtensions(t *testing.T) {
	store := &Store{retention: 30 * 24 * time.Hour}
	for _, spec := range store.tableSpecs() {
		t.Run(spec.name, func(t *testing.T) {
			meta := metadataFor(spec)
			meta.Schema = append(bigquery.Schema(nil), meta.Schema...)
			for i, field := range meta.Schema {
				copy := *field
				copy.Required = false
				meta.Schema[i] = &copy
			}
			meta.Schema = append(meta.Schema,
				&bigquery.FieldSchema{Name: "nullable_tag", Type: bigquery.StringFieldType},
				&bigquery.FieldSchema{Name: "default_tag", Type: bigquery.StringFieldType, Required: true, DefaultValueExpression: "'audit'"},
			)
			require.NoError(t, conforms(spec, meta))
		})
	}
}
