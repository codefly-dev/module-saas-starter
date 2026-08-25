package modulepackage

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func RunGenerators(moduleRoot string, manifest Manifest) error {
	for _, generator := range manifest.Generators {
		if err := runCommand(moduleRoot, "generator", generator); err != nil {
			return err
		}
	}
	return nil
}

func RunConformanceSuites(moduleRoot string, manifest Manifest) error {
	for _, suite := range manifest.Conformance {
		if err := runCommand(moduleRoot, "conformance suite", suite); err != nil {
			return err
		}
	}
	return nil
}

func runCommand(moduleRoot, kind string, command Command) error {
	process := exec.Command(command.Command[0], command.Command[1:]...)
	process.Dir = filepath.Join(moduleRoot, filepath.FromSlash(command.WorkingDirectory))
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "GOWORK=") {
			process.Env = append(process.Env, value)
		}
	}
	process.Env = append(process.Env, "GOWORK=off")
	output, err := process.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %q failed: %w: %s", kind, command.Name, err, strings.TrimSpace(string(output)))
	}
	return nil
}
