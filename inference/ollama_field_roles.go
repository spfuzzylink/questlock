package inference

import (
	"encoding/json"
	"errors"
)

const maxOllamaFieldRolesBytes = 4096

const ollamaFieldRoleDefinitions = "Field role definitions: quantity is a count or amount requested by the task; policy_action is the task's stated handling behavior or action rather than an amount; identifier is a source identifier copied exactly, including numeric-looking codes."

func validateOllamaFieldRoles(options *OllamaOptions) error {
	if options == nil || options.FieldRoles == nil {
		return nil
	}
	if options.Format != "json" || options.OutputTypes == nil || len(options.FieldRoles) < 1 || len(options.FieldRoles) > 32 {
		return errors.New("ollama field_roles requires format=json, output_types, and 1..32 manually declared roles")
	}
	for field, role := range options.FieldRoles {
		typ, exists := options.OutputTypes[field]
		if !ollamaOutputFieldName(field) || !exists {
			return errors.New("ollama field_roles keys must be valid identifiers present in output_types")
		}
		switch role {
		case "quantity":
			if typ != "integer" && typ != "number" {
				return errors.New("ollama quantity roles require integer or number output types")
			}
		case "policy_action", "identifier":
			if typ != "string" {
				return errors.New("ollama policy_action and identifier roles require string output types")
			}
		default:
			return errors.New("ollama field_roles supports only quantity, policy_action, or identifier")
		}
	}
	encoded, err := json.Marshal(options.FieldRoles)
	if err != nil || len(encoded) > maxOllamaFieldRolesBytes {
		return errors.New("ollama field_roles exceeds 4096 serialized bytes")
	}
	return nil
}
