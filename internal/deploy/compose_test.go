package deploy

import (
	"fmt"
	"reflect"
	"sort"
	"testing"
)

// --- ParseCompose: pure-function table tests, no clone/network/DB needed. ---

func TestParseComposeImageService(t *testing.T) {
	yamlDoc := []byte(`
services:
  db:
    image: postgres:16
    ports:
      - "5432:5432"
    environment:
      POSTGRES_PASSWORD: secret
    volumes:
      - pgdata:/var/lib/postgresql/data
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	if len(plan.Services) != 1 {
		t.Fatalf("len(Services) = %d, want 1", len(plan.Services))
	}
	svc := plan.Services[0]
	if svc.Name != "db" || svc.Kind != "image" || svc.Image != "postgres:16" {
		t.Errorf("svc = %+v, want name=db kind=image image=postgres:16", svc)
	}
	if svc.Port != 5432 {
		t.Errorf("Port = %d, want 5432", svc.Port)
	}
	if svc.Env["POSTGRES_PASSWORD"] != "secret" {
		t.Errorf("Env[POSTGRES_PASSWORD] = %q, want secret", svc.Env["POSTGRES_PASSWORD"])
	}
	if svc.VolumePath != "/var/lib/postgresql/data" {
		t.Errorf("VolumePath = %q, want /var/lib/postgresql/data", svc.VolumePath)
	}
	if len(svc.Warnings) != 0 {
		t.Errorf("Warnings = %v, want none", svc.Warnings)
	}
}

func TestParseComposeBuildService(t *testing.T) {
	cases := []struct {
		name    string
		yamlDoc string
		wantCtx string
	}{
		{
			name: "bare string context",
			yamlDoc: `
services:
  web:
    build: ./backend
    ports:
      - "8080:80"
`,
			wantCtx: "backend",
		},
		{
			name: "mapping with default context",
			yamlDoc: `
services:
  web:
    build:
      dockerfile: Dockerfile
    ports:
      - "8080:80"
`,
			wantCtx: "",
		},
		{
			name: "mapping with custom dockerfile and args",
			yamlDoc: `
services:
  web:
    build:
      context: ./app
      dockerfile: Dockerfile.prod
      args:
        FOO: bar
    ports:
      - "8080:80"
`,
			wantCtx: "app",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParseCompose([]byte(tc.yamlDoc), nil)
			if err != nil {
				t.Fatalf("ParseCompose: %v", err)
			}
			if len(plan.Services) != 1 {
				t.Fatalf("len(Services) = %d, want 1", len(plan.Services))
			}
			svc := plan.Services[0]
			if svc.Kind != "git" {
				t.Errorf("Kind = %q, want git", svc.Kind)
			}
			if svc.BuildContext != tc.wantCtx {
				t.Errorf("BuildContext = %q, want %q", svc.BuildContext, tc.wantCtx)
			}
		})
	}
}

func TestParseComposeBuildContextEscape(t *testing.T) {
	yamlDoc := []byte(`
services:
  web:
    build: ../outside
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	svc := plan.Services[0]
	if svc.BuildContext != "" {
		t.Errorf("BuildContext = %q, want empty (fell back to repo root)", svc.BuildContext)
	}
	found := false
	for _, w := range svc.Warnings {
		if w == fmt.Sprintf("build context %q escapes the repo root — building from the repo root instead", "../outside") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want an escape warning", svc.Warnings)
	}
}

func TestParseComposeEnvironmentForms(t *testing.T) {
	cases := []struct {
		name    string
		yamlDoc string
		want    map[string]string
	}{
		{
			name: "mapping form",
			yamlDoc: `
services:
  app:
    image: myapp
    environment:
      FOO: bar
      DEBUG: true
`,
			want: map[string]string{"FOO": "bar", "DEBUG": "true"},
		},
		{
			name: "list form",
			yamlDoc: `
services:
  app:
    image: myapp
    environment:
      - FOO=bar
      - DEBUG=true
`,
			want: map[string]string{"FOO": "bar", "DEBUG": "true"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParseCompose([]byte(tc.yamlDoc), nil)
			if err != nil {
				t.Fatalf("ParseCompose: %v", err)
			}
			got := plan.Services[0].Env
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Env = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseComposeEnvFile(t *testing.T) {
	yamlDoc := []byte(`
services:
  app:
    image: myapp
    env_file: .env
    environment:
      OVERRIDE: from-environment
`)

	t.Run("with reader", func(t *testing.T) {
		reader := func(path string) ([]byte, error) {
			if path != ".env" {
				t.Fatalf("unexpected env_file path %q", path)
			}
			return []byte("FOO=bar\n# comment\n\nOVERRIDE=from-env-file\nQUOTED=\"baz\"\n"), nil
		}
		plan, err := ParseCompose(yamlDoc, reader)
		if err != nil {
			t.Fatalf("ParseCompose: %v", err)
		}
		want := map[string]string{"FOO": "bar", "OVERRIDE": "from-environment", "QUOTED": "baz"}
		if got := plan.Services[0].Env; !reflect.DeepEqual(got, want) {
			t.Errorf("Env = %v, want %v", got, want)
		}
	})

	t.Run("without reader", func(t *testing.T) {
		plan, err := ParseCompose(yamlDoc, nil)
		if err != nil {
			t.Fatalf("ParseCompose: %v", err)
		}
		svc := plan.Services[0]
		if len(svc.Warnings) == 0 {
			t.Error("expected a warning when env_file can't be read")
		}
		if svc.Env["OVERRIDE"] != "from-environment" {
			t.Errorf("Env[OVERRIDE] = %q, want from-environment", svc.Env["OVERRIDE"])
		}
	})
}

func TestParseComposeDependsOn(t *testing.T) {
	cases := []struct {
		name    string
		yamlDoc string
		want    []string
	}{
		{
			name: "list form",
			yamlDoc: `
services:
  web:
    build: .
    depends_on:
      - db
      - cache
  db:
    image: postgres:16
  cache:
    image: redis:7
`,
			want: []string{"cache", "db"},
		},
		{
			name: "mapping form",
			yamlDoc: `
services:
  web:
    build: .
    depends_on:
      db:
        condition: service_healthy
  db:
    image: postgres:16
`,
			want: []string{"db"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := ParseCompose([]byte(tc.yamlDoc), nil)
			if err != nil {
				t.Fatalf("ParseCompose: %v", err)
			}
			var got []string
			for _, l := range plan.Links {
				if l.AppName != "web" {
					t.Errorf("unexpected link source %q", l.AppName)
				}
				got = append(got, l.DependsOnName)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("depends_on = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLinkAlias(t *testing.T) {
	cases := []struct{ in, want string }{
		{"db", "DB"},
		{"my-service", "MY_SERVICE"},
		{"cache_1", "CACHE_1"},
		{"123db", "SVC_123DB"},
	}
	for _, tc := range cases {
		if got := linkAlias(tc.in); got != tc.want {
			t.Errorf("linkAlias(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseComposeNoPortWarnsForImageOnly(t *testing.T) {
	yamlDoc := []byte(`
services:
  worker:
    build: .
  cache:
    image: redis:7
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	byName := map[string]ComposeService{}
	for _, s := range plan.Services {
		byName[s.Name] = s
	}

	if w := byName["worker"].Warnings; len(w) != 0 {
		t.Errorf("git-kind worker should not warn about missing port, got %v", w)
	}
	wantWarn := "no port detected — set one manually before importing"
	found := false
	for _, w := range byName["cache"].Warnings {
		if w == wantWarn {
			found = true
		}
	}
	if !found {
		t.Errorf("cache Warnings = %v, want %q", byName["cache"].Warnings, wantWarn)
	}
}

func TestParseComposeTopLevelVolumesAndNetworks(t *testing.T) {
	yamlDoc := []byte(`
services:
  app:
    image: myapp
    ports:
      - "8080:8080"
volumes:
  pgdata: {}
networks:
  default:
    driver: bridge
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	if len(plan.Warnings) != 2 {
		t.Errorf("plan.Warnings = %v, want 2 entries (volumes + networks)", plan.Warnings)
	}
}

func TestParseComposeProfilesAndExtraVolumes(t *testing.T) {
	yamlDoc := []byte(`
services:
  debugger:
    image: busybox
    ports:
      - "9000:9000"
    profiles: ["debug"]
    volumes:
      - data:/data
      - logs:/var/log
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	svc := plan.Services[0]
	if svc.VolumePath != "/data" {
		t.Errorf("VolumePath = %q, want /data", svc.VolumePath)
	}
	var hasProfileWarn, hasExtraVolumeWarn bool
	for _, w := range svc.Warnings {
		if w == `profile(s) [debug] ignored — service is imported unconditionally` {
			hasProfileWarn = true
		}
		if w == "1 volume mount(s) not imported" {
			hasExtraVolumeWarn = true
		}
	}
	if !hasProfileWarn {
		t.Errorf("Warnings = %v, want a profiles warning", svc.Warnings)
	}
	if !hasExtraVolumeWarn {
		t.Errorf("Warnings = %v, want an extra-volume warning", svc.Warnings)
	}
}

func TestParseComposeServiceWithNeitherImageNorBuild(t *testing.T) {
	yamlDoc := []byte(`
services:
  placeholder: {}
`)
	plan, err := ParseCompose(yamlDoc, nil)
	if err != nil {
		t.Fatalf("ParseCompose: %v", err)
	}
	if len(plan.Services) != 1 {
		t.Fatalf("len(Services) = %d, want 1", len(plan.Services))
	}
	svc := plan.Services[0]
	if svc.Kind != "" {
		t.Errorf("Kind = %q, want empty", svc.Kind)
	}
	if len(svc.Warnings) != 1 {
		t.Errorf("Warnings = %v, want 1 entry", svc.Warnings)
	}
}

func TestNormalizeBuildContext(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantOK  bool
	}{
		{".", "", true},
		{"./", "", true},
		{"", "", true},
		{"./backend", "backend", true},
		{"backend/api", "backend/api", true},
		{"..", "", false},
		{"../sibling", "", false},
		{"/abs/path", "", false},
	}
	for _, tc := range cases {
		got, ok := normalizeBuildContext(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("normalizeBuildContext(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}
