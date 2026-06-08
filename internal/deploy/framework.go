package deploy

import (
	"fmt"
	"os"
	"path/filepath"
)

// frameworkMarkers maps a framework identifier to the marker files that signal
// it at a repo's root, checked in declaration order — the first match wins, so
// more specific ecosystems (e.g. Go) are listed ahead of catch-alls.
var frameworkMarkers = []struct {
	framework string
	files     []string
}{
	{FrameworkGo, []string{"go.mod"}},
	{FrameworkNode, []string{"package.json"}},
	{FrameworkPython, []string{"requirements.txt", "pyproject.toml", "Pipfile"}},
	{FrameworkJava, []string{"pom.xml", "build.gradle", "build.gradle.kts"}},
	{FrameworkRuby, []string{"Gemfile"}},
	{FrameworkPHP, []string{"composer.json"}},
}

// DetectFramework inspects a cloned repository's root directory for marker
// files and returns the matching Framework* identifier, or FrameworkUnknown if
// none match — the signal T13's Buildpacks step uses to pick a builder. Pure
// filesystem inspection (os.Stat only), independently testable against temp
// dirs without a real clone.
func DetectFramework(repoPath string) (string, error) {
	for _, candidate := range frameworkMarkers {
		for _, marker := range candidate.files {
			switch _, err := os.Stat(filepath.Join(repoPath, marker)); {
			case err == nil:
				return candidate.framework, nil
			case os.IsNotExist(err):
				continue
			default:
				return "", fmt.Errorf("deploy: stat %s: %w", marker, err)
			}
		}
	}
	return FrameworkUnknown, nil
}
