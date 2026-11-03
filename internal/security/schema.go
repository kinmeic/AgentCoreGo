package security

import (
	"fmt"

	"github.com/xeipuuv/gojsonschema"
)

// ValidateJSONSchema resolves references only within the supplied schema. Tool
// and benchmark schemas are untrusted; validation must not perform HTTP requests
// or read local files before authorization (or outside a workspace).
func ValidateJSONSchema(schema, document any) (*gojsonschema.Result, error) {
	return gojsonschema.Validate(offlineSchemaLoader{gojsonschema.NewGoLoader(schema)}, gojsonschema.NewGoLoader(document))
}

type offlineSchemaLoader struct{ gojsonschema.JSONLoader }

func (offlineSchemaLoader) LoaderFactory() gojsonschema.JSONLoaderFactory {
	return offlineSchemaFactory{}
}

type offlineSchemaFactory struct{}

func (offlineSchemaFactory) New(source string) gojsonschema.JSONLoader {
	return deniedSchemaReference{gojsonschema.NewReferenceLoader(source)}
}

type deniedSchemaReference struct{ gojsonschema.JSONLoader }

func (deniedSchemaReference) LoadJSON() (any, error) {
	return nil, fmt.Errorf("external JSON Schema references are disabled; embed referenced definitions in the schema")
}
