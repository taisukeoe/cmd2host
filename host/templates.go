// templates.go provides embedded template files for project configuration.
package main

import (
	"embed"
	"fmt"
	"path/filepath"
	"strings"
)

//go:embed templates/*.json
var templatesFS embed.FS

// ListTemplates returns a list of available template names (without extension)
func ListTemplates() ([]string, error) {
	entries, err := templatesFS.ReadDir("templates")
	if err != nil {
		return nil, fmt.Errorf("failed to read templates directory: %w", err)
	}

	var templates []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".json") {
			// Remove .json extension
			templates = append(templates, strings.TrimSuffix(name, ".json"))
		}
	}
	return templates, nil
}

// GetTemplate returns the content of a template by name
func GetTemplate(name string) ([]byte, error) {
	filename := name + ".json"
	path := filepath.Join("templates", filename)
	data, err := templatesFS.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %q: %w", name, err)
	}
	return data, nil
}
