package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/composition"
)

type paths []string

func (values *paths) String() string {
	return fmt.Sprint([]string(*values))
}

func (values *paths) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	flags := flag.NewFlagSet("module-compose", flag.ContinueOnError)
	input := flags.String("input", os.Getenv("CODEFLY_COMPOSITION_INPUT"), "Core composition input JSON")
	moduleRoot := flags.String("module", "..", "module package root")
	output := flags.String("output", "..", "composed module projection root")
	var frontend, settings, permissions, fixtures, topology paths
	flags.Var(&frontend, "frontend", "frontend contribution document")
	flags.Var(&settings, "settings", "settings contribution document")
	flags.Var(&permissions, "permissions", "permissions contribution document")
	flags.Var(&fixtures, "fixtures", "fixtures contribution document")
	flags.Var(&topology, "topology", "topology contribution document")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	return composition.Generate(composition.Options{
		InputPath:   *input,
		ModuleRoot:  *moduleRoot,
		OutputRoot:  *output,
		Frontend:    frontend,
		Settings:    settings,
		Permissions: permissions,
		Fixtures:    fixtures,
		Topology:    topology,
	})
}
