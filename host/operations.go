// operations.go provides operation template definitions and parameter handling.
// Operations define pre-approved command patterns with typed parameters.
package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Operation defines a pre-approved command template
type Operation struct {
	Command      string                 `json:"command"`       // e.g., "gh", "git"
	ArgsTemplate []string               `json:"args_template"` // e.g., ["pr", "view", "{number}"]
	Params       map[string]ParamSchema `json:"params"`        // Parameter schemas
	AllowedFlags []string               `json:"allowed_flags"` // e.g., ["--state", "--limit"]
	Description  string                 `json:"description"`   // Human-readable description
}

// ItemsSchema defines the schema for array items
type ItemsSchema struct {
	Type string `json:"type"`
}

// ParamSchema defines validation rules for a parameter
type ParamSchema struct {
	Type      string       `json:"type"`                // "string", "integer", "array"
	Pattern   string       `json:"pattern,omitempty"`   // Regex pattern for strings
	MinLength int          `json:"minLength,omitempty"` // Min length for strings
	MaxLength int          `json:"maxLength,omitempty"` // Max length for strings
	Min       int          `json:"min,omitempty"`       // Min value for integers
	Max       int          `json:"max,omitempty"`       // Max value for integers
	Items     *ItemsSchema `json:"items,omitempty"`     // For array types

	// Compiled pattern (not serialized)
	compiledPattern *regexp.Regexp
}

// ParamValue represents a parameter value that can be string, int, or []string
type ParamValue interface{}

// OperationRequest represents a request to execute an operation
type OperationRequest struct {
	RequestID string                 `json:"request_id,omitempty"`
	Operation string                 `json:"operation"`
	Params    map[string]ParamValue  `json:"params"`
	Flags     []string               `json:"flags,omitempty"`
	Token     string                 `json:"token"`
}

// OperationResponse represents the response from an operation
type OperationResponse struct {
	RequestID    string  `json:"request_id,omitempty"`
	ExitCode     int     `json:"exit_code"`
	Stdout       string  `json:"stdout"`
	Stderr       string  `json:"stderr"`
	DeniedReason *string `json:"denied_reason"`
}

// CompilePatterns compiles regex patterns in parameter schemas
func (op *Operation) CompilePatterns() error {
	for name, schema := range op.Params {
		if schema.Pattern != "" {
			re, err := regexp.Compile(schema.Pattern)
			if err != nil {
				return fmt.Errorf("invalid pattern for param %s: %w", name, err)
			}
			schema.compiledPattern = re
			op.Params[name] = schema
		}
	}
	return nil
}

// ValidateParams validates parameters against the operation schema
func (op *Operation) ValidateParams(params map[string]ParamValue) error {
	// Check for unknown parameters
	for name := range params {
		if _, exists := op.Params[name]; !exists {
			return fmt.Errorf("unknown parameter: %s", name)
		}
	}

	// Validate each defined parameter
	for name, schema := range op.Params {
		value, exists := params[name]
		if !exists {
			// Parameter is optional if not provided (template may have defaults)
			continue
		}

		if err := validateParamValue(name, value, schema); err != nil {
			return err
		}
	}

	return nil
}

// validateParamValue validates a single parameter value against its schema
func validateParamValue(name string, value ParamValue, schema ParamSchema) error {
	switch schema.Type {
	case "string":
		str, ok := value.(string)
		if !ok {
			return fmt.Errorf("param %s: expected string, got %T", name, value)
		}
		if schema.MinLength > 0 && len(str) < schema.MinLength {
			return fmt.Errorf("param %s: length %d below minimum %d", name, len(str), schema.MinLength)
		}
		if schema.MaxLength > 0 && len(str) > schema.MaxLength {
			return fmt.Errorf("param %s: length %d exceeds maximum %d", name, len(str), schema.MaxLength)
		}
		if schema.compiledPattern != nil && !schema.compiledPattern.MatchString(str) {
			return fmt.Errorf("param %s: value does not match pattern %s", name, schema.Pattern)
		}

	case "integer":
		var intVal int
		switch v := value.(type) {
		case int:
			intVal = v
		case float64:
			intVal = int(v) // JSON unmarshals numbers as float64
		default:
			return fmt.Errorf("param %s: expected integer, got %T", name, value)
		}
		if schema.Min > 0 && intVal < schema.Min {
			return fmt.Errorf("param %s: value %d below minimum %d", name, intVal, schema.Min)
		}
		if schema.Max > 0 && intVal > schema.Max {
			return fmt.Errorf("param %s: value %d exceeds maximum %d", name, intVal, schema.Max)
		}

	case "array":
		arr, ok := value.([]interface{})
		if !ok {
			// Also accept []string
			if strArr, ok := value.([]string); ok {
				for _, s := range strArr {
					if schema.Items != nil && schema.Items.Type == "string" {
						// Valid
						_ = s
					}
				}
				return nil
			}
			return fmt.Errorf("param %s: expected array, got %T", name, value)
		}
		if schema.Items != nil && schema.Items.Type == "string" {
			for i, item := range arr {
				if _, ok := item.(string); !ok {
					return fmt.Errorf("param %s[%d]: expected string, got %T", name, i, item)
				}
			}
		}

	default:
		return fmt.Errorf("param %s: unknown type %s", name, schema.Type)
	}

	return nil
}

// ValidateFlags validates that all provided flags are in the allowed list
func (op *Operation) ValidateFlags(flags []string) error {
	if len(op.AllowedFlags) == 0 && len(flags) > 0 {
		return fmt.Errorf("no flags allowed for this operation")
	}

	allowedSet := make(map[string]bool)
	for _, f := range op.AllowedFlags {
		allowedSet[f] = true
	}

	skipNext := false
	for _, flag := range flags {
		// Skip flag values (the element after a flag like --state)
		if skipNext {
			skipNext = false
			continue
		}

		// Skip elements that don't look like flags (don't start with -)
		if !strings.HasPrefix(flag, "-") {
			continue
		}

		// Extract flag name (handle --flag=value)
		flagName := flag
		if idx := strings.Index(flag, "="); idx > 0 {
			flagName = flag[:idx]
		} else {
			// Flag without =, next element is the value
			skipNext = true
		}

		if !allowedSet[flagName] {
			return fmt.Errorf("flag not allowed: %s", flagName)
		}
	}

	return nil
}

// BuildArgs builds the final argument list by expanding the template
func (op *Operation) BuildArgs(params map[string]ParamValue, flags []string, profileEnv map[string]string) ([]string, error) {
	var args []string

	for _, tmpl := range op.ArgsTemplate {
		if strings.HasPrefix(tmpl, "{") && strings.HasSuffix(tmpl, "}") {
			// Template placeholder
			paramName := tmpl[1 : len(tmpl)-1]

			// Check for special profile-injected values
			if profileEnv != nil {
				if val, ok := profileEnv[paramName]; ok {
					args = append(args, val)
					continue
				}
			}

			value, exists := params[paramName]
			if !exists {
				return nil, fmt.Errorf("missing required parameter: %s", paramName)
			}

			expanded, err := expandParamValue(value)
			if err != nil {
				return nil, fmt.Errorf("param %s: %w", paramName, err)
			}
			args = append(args, expanded...)
		} else {
			// Literal argument
			args = append(args, tmpl)
		}
	}

	// Append validated flags
	args = append(args, flags...)

	return args, nil
}

// expandParamValue converts a parameter value to string arguments
func expandParamValue(value ParamValue) ([]string, error) {
	switch v := value.(type) {
	case string:
		return []string{v}, nil
	case int:
		return []string{fmt.Sprintf("%d", v)}, nil
	case float64:
		return []string{fmt.Sprintf("%d", int(v))}, nil
	case []interface{}:
		var result []string
		for _, item := range v {
			if s, ok := item.(string); ok {
				result = append(result, s)
			} else {
				return nil, fmt.Errorf("array item is not a string: %T", item)
			}
		}
		return result, nil
	case []string:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported value type: %T", value)
	}
}
