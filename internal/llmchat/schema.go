// Schema derivation: turns a Go result type into the schema a provider needs
// so a model answers in that exact shape instead of in prose.
package llmchat

import (
	"fmt"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/genai"
)

// SchemaFor derives a provider-ready schema from a Go type, so callers do not
// hand-maintain a second copy of every result struct.
func SchemaFor[T any]() (*genai.Schema, error) {
	derived, err := jsonschema.For[T](&jsonschema.ForOptions{IgnoreInvalidTypes: true})
	if err != nil {
		return nil, fmt.Errorf("llmchat: derive schema: %w", err)
	}
	converted := convertSchema(derived, derived, 0)
	if converted == nil {
		return nil, fmt.Errorf("llmchat: derived schema is empty")
	}
	return converted, nil
}

// maxSchemaDepth stops a self-referencing type from recursing forever.
const maxSchemaDepth = 12

func convertSchema(node, root *jsonschema.Schema, depth int) *genai.Schema {
	if node == nil || depth > maxSchemaDepth {
		return nil
	}
	if node.Ref != "" {
		if resolved := resolveRef(node.Ref, root); resolved != nil {
			return convertSchema(resolved, root, depth+1)
		}
		return &genai.Schema{Type: genai.TypeObject}
	}

	out := &genai.Schema{Type: schemaType(node), Description: node.Description}
	for _, value := range node.Enum {
		if text, ok := value.(string); ok {
			out.Enum = append(out.Enum, text)
		}
	}
	if out.Type == genai.TypeArray {
		out.Items = convertSchema(node.Items, root, depth+1)
		if out.Items == nil {
			out.Items = &genai.Schema{Type: genai.TypeString}
		}
	}
	if len(node.Properties) > 0 {
		out.Type = genai.TypeObject
		out.Properties = map[string]*genai.Schema{}
		for name, property := range node.Properties {
			if converted := convertSchema(property, root, depth+1); converted != nil {
				out.Properties[name] = converted
			}
		}
		out.Required = node.Required
	}
	return out
}

func resolveRef(ref string, root *jsonschema.Schema) *jsonschema.Schema {
	const prefix = "#/$defs/"
	if !strings.HasPrefix(ref, prefix) || root == nil {
		return nil
	}
	return root.Defs[strings.TrimPrefix(ref, prefix)]
}

// schemaType maps a JSON Schema type name onto the provider enum, defaulting to
// object because that is the only shape a top-level answer can take.
func schemaType(node *jsonschema.Schema) genai.Type {
	name := node.Type
	if name == "" && len(node.Types) > 0 {
		for _, candidate := range node.Types {
			if candidate != "null" {
				name = candidate
				break
			}
		}
	}
	switch name {
	case "string":
		return genai.TypeString
	case "integer":
		return genai.TypeInteger
	case "number":
		return genai.TypeNumber
	case "boolean":
		return genai.TypeBoolean
	case "array":
		return genai.TypeArray
	default:
		return genai.TypeObject
	}
}
