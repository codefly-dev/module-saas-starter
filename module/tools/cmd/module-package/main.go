package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/codefly-dev/agents/modules/saas-starter/module/tools/modulepackage"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("usage: module-package <validate|build|run-generators|run-conformance|verify-release-policy|verify-release-ref>")
	}
	switch arguments[0] {
	case "validate":
		flags := flag.NewFlagSet("validate", flag.ContinueOnError)
		moduleRoot := flags.String("module", ".", "module package root")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		manifest, err := modulepackage.ReadManifest(*moduleRoot)
		if err != nil {
			return err
		}
		fmt.Printf("%s@%s\n", manifest.ID, manifest.Version)
		return nil
	case "build":
		flags := flag.NewFlagSet("build", flag.ContinueOnError)
		repositoryRoot := flags.String("repository-root", ".", "git repository root")
		output := flags.String("output", "release", "release asset directory")
		commit := flags.String("commit", "", "exact release commit")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		metadata, err := modulepackage.Build(modulepackage.BuildOptions{
			RepositoryRoot: *repositoryRoot,
			OutputDir:      *output,
			Commit:         *commit,
		})
		if err != nil {
			return err
		}
		fmt.Println(metadata.Artifact.Digest)
		return nil
	case "run-generators", "run-conformance":
		flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
		moduleRoot := flags.String("module", ".", "module package root")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		manifest, err := modulepackage.ReadManifest(*moduleRoot)
		if err != nil {
			return err
		}
		if arguments[0] == "run-generators" {
			return modulepackage.RunGenerators(*moduleRoot, manifest)
		}
		return modulepackage.RunConformanceSuites(*moduleRoot, manifest)
	case "verify-release-ref":
		flags := flag.NewFlagSet("verify-release-ref", flag.ContinueOnError)
		moduleRoot := flags.String("module", "module", "module package root")
		tag := flags.String("tag", "", "release tag")
		commit := flags.String("commit", "", "release commit")
		remoteRefsPath := flags.String("remote-refs", "", "git ls-remote output file")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		manifest, err := modulepackage.ReadManifest(*moduleRoot)
		if err != nil {
			return err
		}
		body, err := os.ReadFile(filepath.Clean(*remoteRefsPath))
		if err != nil {
			return fmt.Errorf("read remote refs: %w", err)
		}
		return modulepackage.ValidateReleaseRef(manifest, *tag, *commit, string(body))
	case "verify-release-policy":
		flags := flag.NewFlagSet("verify-release-policy", flag.ContinueOnError)
		settingsPath := flags.String("settings", "", "GitHub immutable release settings response")
		if err := flags.Parse(arguments[1:]); err != nil {
			return err
		}
		body, err := os.ReadFile(filepath.Clean(*settingsPath))
		if err != nil {
			return fmt.Errorf("read immutable release settings: %w", err)
		}
		return modulepackage.ValidateImmutableReleaseSettings(body)
	default:
		return fmt.Errorf("unknown command %q", arguments[0])
	}
}
