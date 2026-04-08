package main

import (
	"context"
	"embed"
	"fmt"
	"os"

	"github.com/codefly-dev/core/shared"
	"github.com/codefly-dev/core/templates"
	"github.com/codefly-dev/core/wool"
)

//go:embed all:templates/factory
var factoryFS embed.FS

// CreateInput holds the input for creating a module from this template
type CreateInput struct {
	Name string
}

// Create scaffolds a new module from the saas-starter template
func Create(ctx context.Context, dir string, input *CreateInput) error {
	w := wool.Get(ctx).In("saas-starter::Create")

	_, err := shared.CheckDirectoryOrCreate(ctx, dir)
	if err != nil {
		return w.Wrapf(err, "cannot create module directory")
	}

	t := templates.Templator{
		PathSelect:   shared.NewIgnore("node_modules", ".next"),
		NameReplacer: templates.CutTemplateSuffix{},
	}
	err = t.CopyAndApply(ctx, shared.Embed(factoryFS), "templates/factory", dir, input)
	if err != nil {
		return w.Wrapf(err, "cannot apply module template")
	}

	w.Info("module created", wool.Field("name", input.Name))
	return nil
}

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintf(os.Stderr, "Usage: saas-starter <dir> <name>\n")
		os.Exit(1)
	}
	ctx := context.Background()
	dir := os.Args[1]
	name := os.Args[2]

	if err := Create(ctx, dir, &CreateInput{Name: name}); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
