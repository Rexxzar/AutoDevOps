package deploy

import (
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ComposeService is one service parsed from a docker-compose file, mapped
// onto AutoDevOps' deployment_apps shape — services with an "image:" become
// kind="image" apps (Phase 4's image-kind path, Port/Env/VolumePath feed
// appCreateRequest directly), services with a "build:" become kind="git"
// apps scoped to BuildContext (Phase 1's contextDir param, threaded through
// buildImage). Warnings call out constructs this import doesn't model.
type ComposeService struct {
	Name         string            `json:"name"`
	Kind         string            `json:"kind"` // "image" | "git"
	Image        string            `json:"image,omitempty"`
	BuildContext string            `json:"build_context,omitempty"`
	Port         int               `json:"port,omitempty"`
	Env          map[string]string `json:"env,omitempty"`
	VolumePath   string            `json:"volume_path,omitempty"`
	Warnings     []string          `json:"warnings,omitempty"`
}

// ComposeLink is a suggested deployment_app_links row derived from a
// service's "depends_on" — surfaced for the user to confirm, never created
// automatically.
type ComposeLink struct {
	AppName       string `json:"app_name"`
	DependsOnName string `json:"depends_on_name"`
	Alias         string `json:"alias"`
}

// ComposePlan is PreviewCompose's result.
type ComposePlan struct {
	Services []ComposeService `json:"services"`
	Links    []ComposeLink    `json:"links"`
	Warnings []string         `json:"warnings,omitempty"`
}

// composeFile is the subset of docker-compose's schema this import
// understands. Fields decode into interface{} because docker-compose allows
// several shorthand forms (e.g. "build:" as a bare string or a mapping,
// "environment:" as a list or a mapping) — each is normalized by the
// parseXxx helpers below.
type composeFile struct {
	Services map[string]composeServiceRaw `yaml:"services"`
	Volumes  map[string]interface{}       `yaml:"volumes"`
	Networks map[string]interface{}       `yaml:"networks"`
}

type composeServiceRaw struct {
	Image       string        `yaml:"image"`
	Build       interface{}   `yaml:"build"`
	Ports       []interface{} `yaml:"ports"`
	Expose      []interface{} `yaml:"expose"`
	Environment interface{}   `yaml:"environment"`
	EnvFile     interface{}   `yaml:"env_file"`
	DependsOn   interface{}   `yaml:"depends_on"`
	Volumes     []interface{} `yaml:"volumes"`
	Networks    interface{}   `yaml:"networks"`
	Profiles    []string      `yaml:"profiles"`
}

// envFileReader fetches the contents of an env_file path relative to the
// repo root — PreviewCompose wires this to the cloned workspace; nil (e.g.
// in unit tests) skips env_file resolution with a warning instead.
type envFileReader func(path string) ([]byte, error)

// ParseCompose maps a docker-compose YAML document onto a ComposePlan. Pure
// function over the file's bytes plus an optional env_file reader — no
// network or DB access, so it's directly unit-testable against fixture YAML.
func ParseCompose(data []byte, readEnvFile envFileReader) (*ComposePlan, error) {
	var cf composeFile
	if err := yaml.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("deploy: parse docker-compose file: %w", err)
	}

	plan := &ComposePlan{}

	if len(cf.Volumes) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%d top-level volume(s) defined — not imported; only a single volume_path per image-kind service is supported", len(cf.Volumes)))
	}
	if len(cf.Networks) > 0 {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf(
			"%d top-level network(s) defined — not imported; AutoDevOps gives each stack its own network automatically", len(cf.Networks)))
	}

	names := make([]string, 0, len(cf.Services))
	for name := range cf.Services {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		raw := cf.Services[name]
		svc := ComposeService{Name: name}

		buildCtx, dockerfile, hasArgs, hasBuild := parseBuild(raw.Build)
		switch {
		case hasBuild:
			svc.Kind = "git"
			normalized, ok := normalizeBuildContext(buildCtx)
			if !ok {
				svc.Warnings = append(svc.Warnings, fmt.Sprintf("build context %q escapes the repo root — building from the repo root instead", buildCtx))
			}
			svc.BuildContext = normalized
			if dockerfile != "" && !strings.EqualFold(dockerfile, "Dockerfile") {
				svc.Warnings = append(svc.Warnings, fmt.Sprintf(
					"custom Dockerfile %q not supported — expected a \"Dockerfile\" in the build context (falls back to Paketo Buildpacks if absent)", dockerfile))
			}
			if hasArgs {
				svc.Warnings = append(svc.Warnings, "build.args not supported — ignored")
			}
			if raw.Image != "" {
				svc.Warnings = append(svc.Warnings, fmt.Sprintf("image %q ignored — service has a build: section and will be built from source", raw.Image))
			}
		case raw.Image != "":
			svc.Kind = "image"
			svc.Image = raw.Image
		default:
			svc.Warnings = append(svc.Warnings, "service has neither image nor build — skipped")
			plan.Services = append(plan.Services, svc)
			continue
		}

		if port, ok := firstPort(raw.Ports, raw.Expose); ok {
			svc.Port = port
		} else if svc.Kind == "image" {
			svc.Warnings = append(svc.Warnings, "no port detected — set one manually before importing")
		}

		if env := resolveEnv(raw, readEnvFile, &svc); len(env) > 0 {
			svc.Env = env
		}

		if len(raw.Volumes) > 0 {
			extra := len(raw.Volumes)
			if svc.Kind == "image" {
				if vp, ok := firstVolumeTarget(raw.Volumes[0]); ok {
					svc.VolumePath = vp
					extra--
				}
			}
			if extra > 0 {
				svc.Warnings = append(svc.Warnings, fmt.Sprintf("%d volume mount(s) not imported", extra))
			}
		}

		if hasInterfaceValue(raw.Networks) {
			svc.Warnings = append(svc.Warnings, "custom network config not imported — AutoDevOps gives each stack its own network automatically")
		}

		if len(raw.Profiles) > 0 {
			svc.Warnings = append(svc.Warnings, fmt.Sprintf("profile(s) %v ignored — service is imported unconditionally", raw.Profiles))
		}

		plan.Services = append(plan.Services, svc)

		for _, dep := range parseDependsOn(raw.DependsOn) {
			plan.Links = append(plan.Links, ComposeLink{
				AppName:       name,
				DependsOnName: dep,
				Alias:         linkAlias(dep),
			})
		}
	}

	return plan, nil
}

// resolveEnv merges a service's "environment:" with its "env_file:" contents
// (environment: takes precedence, matching compose semantics), appending a
// warning to svc if env_file entries can't be resolved.
func resolveEnv(raw composeServiceRaw, readEnvFile envFileReader, svc *ComposeService) map[string]string {
	env := parseEnvironment(raw.Environment)

	files := parseEnvFile(raw.EnvFile)
	if len(files) == 0 {
		return env
	}
	if readEnvFile == nil {
		svc.Warnings = append(svc.Warnings, fmt.Sprintf("env_file %v not read — pass values via environment instead", files))
		return env
	}

	merged := map[string]string{}
	for _, f := range files {
		content, err := readEnvFile(f)
		if err != nil {
			svc.Warnings = append(svc.Warnings, fmt.Sprintf("env_file %q could not be read: %v", f, err))
			continue
		}
		for k, v := range parseDotenv(content) {
			merged[k] = v
		}
	}
	for k, v := range env {
		merged[k] = v
	}
	return merged
}

// parseBuild normalizes docker-compose's "build:", which may be a bare
// string (the context path) or a mapping with context/dockerfile/args.
func parseBuild(v interface{}) (buildContext, dockerfile string, hasArgs, ok bool) {
	switch b := v.(type) {
	case string:
		return b, "", false, true
	case map[string]interface{}:
		buildContext = "."
		if c, ok2 := b["context"].(string); ok2 && c != "" {
			buildContext = c
		}
		if d, ok2 := b["dockerfile"].(string); ok2 {
			dockerfile = d
		}
		hasArgs = hasInterfaceValue(b["args"])
		return buildContext, dockerfile, hasArgs, true
	default:
		return "", "", false, false
	}
}

// normalizeBuildContext cleans a compose build-context path into the form
// deployment_apps.build_context expects: "" means the repo root (a bare "."
// or "./" collapses to ""), relative to the repo root with any "./" prefix
// stripped. ok is false if the path escapes the repo root (e.g. "..") — the
// caller falls back to the repo root in that case.
func normalizeBuildContext(p string) (string, bool) {
	cleaned := path.Clean(p)
	if cleaned == "." || cleaned == "" {
		return "", true
	}
	if path.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", false
	}
	return strings.TrimPrefix(cleaned, "./"), true
}

// firstPort extracts the container-side port from docker-compose's "ports"
// (HOST:CONTAINER, CONTAINER, or CONTAINER/proto, optionally bind-address
// prefixed) or "expose" (CONTAINER or CONTAINER/proto) entries. "ports" is
// preferred — it signals the service is meant to be externally reachable.
func firstPort(ports, expose []interface{}) (int, bool) {
	if p, ok := portFromEntries(ports, true); ok {
		return p, true
	}
	return portFromEntries(expose, false)
}

func portFromEntries(entries []interface{}, splitHostContainer bool) (int, bool) {
	for _, e := range entries {
		s := fmt.Sprintf("%v", e)
		if splitHostContainer {
			parts := strings.Split(s, ":")
			s = parts[len(parts)-1]
		}
		s = strings.SplitN(s, "/", 2)[0]
		if p, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && p > 0 && p <= 65535 {
			return p, true
		}
	}
	return 0, false
}

// parseEnvironment normalizes docker-compose's "environment:", which may be
// a mapping (KEY: value) or a list of "KEY=value" strings.
func parseEnvironment(v interface{}) map[string]string {
	env := map[string]string{}
	switch e := v.(type) {
	case map[string]interface{}:
		for k, val := range e {
			env[k] = fmt.Sprintf("%v", val)
		}
	case []interface{}:
		for _, item := range e {
			if k, val, ok := strings.Cut(fmt.Sprintf("%v", item), "="); ok {
				env[k] = val
			}
		}
	}
	return env
}

// parseEnvFile normalizes docker-compose's "env_file:", a bare string or a
// list of strings — paths relative to the compose file's directory.
func parseEnvFile(v interface{}) []string {
	switch e := v.(type) {
	case string:
		return []string{e}
	case []interface{}:
		files := make([]string, 0, len(e))
		for _, item := range e {
			files = append(files, fmt.Sprintf("%v", item))
		}
		return files
	default:
		return nil
	}
}

// parseDotenv parses simple "KEY=value" lines from an env_file's contents,
// skipping blank lines and "#"-comments — matches docker-compose's basic
// env_file format (no variable interpolation/expansion).
func parseDotenv(data []byte) map[string]string {
	env := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			env[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return env
}

// parseDependsOn normalizes docker-compose's "depends_on:", which may be a
// list of service names or (long syntax) a mapping of name -> {condition: ...}.
func parseDependsOn(v interface{}) []string {
	switch d := v.(type) {
	case []interface{}:
		names := make([]string, 0, len(d))
		for _, item := range d {
			names = append(names, fmt.Sprintf("%v", item))
		}
		return names
	case map[string]interface{}:
		names := make([]string, 0, len(d))
		for k := range d {
			names = append(names, k)
		}
		sort.Strings(names)
		return names
	default:
		return nil
	}
}

// firstVolumeTarget extracts the container-side path from a docker-compose
// "volumes:" entry in short syntax ("source:target" or "source:target:ro") —
// long (mapping) syntax isn't handled, callers fall back to a warning.
func firstVolumeTarget(v interface{}) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 {
		return "", false
	}
	return parts[1], true
}

// linkAlias derives an AUTODEVOPS_<ALIAS>_* env var prefix (see
// internal/api/handlers.go's aliasPattern) from a docker-compose service
// name: uppercased, with non-alphanumeric runs collapsed to underscores, and
// prefixed with "SVC_" if it wouldn't otherwise start with a letter.
func linkAlias(serviceName string) string {
	var b strings.Builder
	for _, r := range serviceName {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 32)
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	alias := b.String()
	if alias == "" || (alias[0] >= '0' && alias[0] <= '9') {
		alias = "SVC_" + alias
	}
	return alias
}

// hasInterfaceValue reports whether a YAML-decoded interface{} carries any
// content — nil, an empty mapping, and an empty sequence all report false.
func hasInterfaceValue(v interface{}) bool {
	switch x := v.(type) {
	case nil:
		return false
	case map[string]interface{}:
		return len(x) > 0
	case []interface{}:
		return len(x) > 0
	default:
		return true
	}
}
