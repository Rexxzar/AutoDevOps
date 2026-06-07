package terraform

import (
	"bytes"
	"embed"
	"fmt"
	"text/template"

	"github.com/hashicorp/hcl/v2/hclwrite"
)

//go:embed templates/*/*.tf.tmpl
var templateFS embed.FS

// Engine renders registered HCL templates from typed, validated parameters
// and returns canonically-formatted output suitable for preview or for
// terraform-exec to plan/apply (T10).
type Engine struct {
	registry map[TemplateID]Definition
	parsed   map[TemplateID]*template.Template
}

// New parses the embedded template for each definition and builds the engine.
// Definitions are supplied by callers (T08/T09 register the AWS/Azure stacks)
// so this package stays free of provider-specific knowledge.
func New(defs []Definition) (*Engine, error) {
	registry := make(map[TemplateID]Definition, len(defs))
	parsed := make(map[TemplateID]*template.Template, len(defs))

	for _, def := range defs {
		content, err := templateFS.ReadFile(def.Path)
		if err != nil {
			return nil, fmt.Errorf("terraform: load template %q: %w", def.ID, err)
		}

		tmpl, err := template.New(string(def.ID)).Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("terraform: parse template %q: %w", def.ID, err)
		}

		registry[def.ID] = def
		parsed[def.ID] = tmpl
	}

	return &Engine{registry: registry, parsed: parsed}, nil
}

// Render validates params, executes the named template against them, and
// returns canonically-formatted HCL. The returned bytes are what gets shown
// to the user in the preview step before any plan/apply.
func (e *Engine) Render(id TemplateID, params Params) ([]byte, error) {
	tmpl, ok := e.parsed[id]
	if !ok {
		return nil, fmt.Errorf("terraform: render %q: unknown template", id)
	}

	if err := params.Validate(); err != nil {
		return nil, fmt.Errorf("terraform: render %q: invalid params: %w", id, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, params); err != nil {
		return nil, fmt.Errorf("terraform: render %q: execute template: %w", id, err)
	}

	return hclwrite.Format(buf.Bytes()), nil
}

// Definition looks up a registered template's metadata by ID.
func (e *Engine) Definition(id TemplateID) (Definition, bool) {
	def, ok := e.registry[id]
	return def, ok
}
