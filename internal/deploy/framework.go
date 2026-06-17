package deploy

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Declared framework identifiers — set by the user at app-creation time to
// enable Procfile auto-generation and framework-specific DB env var mapping.
// These are more granular than the FrameworkXxx constants (which Paketo detects
// from file markers) — e.g. both "laravel" and "php" map to FrameworkPHP.
const (
	DeclaredFrameworkReact   = "react"
	DeclaredFrameworkVite    = "vite"
	DeclaredFrameworkNextJS  = "nextjs"
	DeclaredFrameworkNode    = "nodejs"
	DeclaredFrameworkNestJS  = "nestjs"
	DeclaredFrameworkLaravel = "laravel"
	DeclaredFrameworkDjango  = "django"
	DeclaredFrameworkRails   = "rails"
	DeclaredFrameworkPHP     = "php"
)

// DeclaredFrameworkLabels maps declared_framework identifiers to human-readable
// labels for display in the dashboard.
var DeclaredFrameworkLabels = map[string]string{
	DeclaredFrameworkReact:   "React",
	DeclaredFrameworkVite:    "Vite",
	DeclaredFrameworkNextJS:  "Next.js",
	DeclaredFrameworkNode:    "Node.js",
	DeclaredFrameworkNestJS:  "NestJS",
	DeclaredFrameworkLaravel: "Laravel",
	DeclaredFrameworkDjango:  "Django",
	DeclaredFrameworkRails:   "Rails",
	DeclaredFrameworkPHP:     "PHP",
}

// ProcfileForFramework returns the Procfile content for a declared framework,
// and ok=true when a Procfile should be auto-written. Returns ok=false for
// frameworks that Paketo handles on its own (React, Vite, Next.js) or where
// the entry point is too variable to guess (Node.js, plain PHP).
func ProcfileForFramework(fw string) (content string, ok bool) {
	switch fw {
	case DeclaredFrameworkLaravel:
		return "web: php artisan migrate --force && php artisan serve --host=0.0.0.0 --port=8080", true
	case DeclaredFrameworkNestJS:
		return "web: node dist/main.js", true
	case DeclaredFrameworkDjango:
		return "web: python manage.py migrate && gunicorn wsgi:application --bind 0.0.0.0:8080", true
	case DeclaredFrameworkRails:
		return "web: bundle exec rails db:migrate && bundle exec rails server -b 0.0.0.0 -p 8080", true
	default:
		return "", false
	}
}

// dbImageCreds extracts the DB username, password, database name, and connection
// scheme from a DB image app's decrypted env-var map, dispatching on the image
// reference prefix (postgres:*, mysql:*, mongo:*, redis:*).
func dbImageCreds(imageRef string, envVars map[string]string) (user, pass, db, scheme string) {
	switch {
	case strings.HasPrefix(imageRef, "postgres:"):
		return envVars["POSTGRES_USER"], envVars["POSTGRES_PASSWORD"], envVars["POSTGRES_DB"], "postgresql"
	case strings.HasPrefix(imageRef, "mysql:"):
		return envVars["MYSQL_USER"], envVars["MYSQL_PASSWORD"], envVars["MYSQL_DATABASE"], "mysql"
	case strings.HasPrefix(imageRef, "mongo:"):
		return envVars["MONGO_INITDB_ROOT_USERNAME"], envVars["MONGO_INITDB_ROOT_PASSWORD"], envVars["MONGO_INITDB_DATABASE"], "mongodb"
	case strings.HasPrefix(imageRef, "redis:"):
		return "", "", "", "redis"
	default:
		return "", "", "", ""
	}
}

// applyFrameworkEnv maps the generic AUTODEVOPS_<ALIAS>_* env vars (set by
// resolveLinks) to framework-specific env vars expected by common frameworks.
// It adds vars but never removes or overrides existing ones — safe to call even
// when the consumer already has manually configured env vars.
//
// For each link alias that has HOST+USER+PASSWORD+DATABASE+SCHEME set, the
// function emits framework-specific vars:
//   - laravel/php: DB_HOST, DB_PORT, DB_CONNECTION, DB_DATABASE, DB_USERNAME, DB_PASSWORD
//   - nestjs/django/rails/nodejs: DATABASE_URL=<scheme>://user:pass@host:port/db
//
// The first alias with a complete credential set wins (DB_HOST / DATABASE_URL
// are only set once; subsequent aliases are skipped to avoid accidental
// overwriting when multiple DB links exist).
func applyFrameworkEnv(fw string, env map[string]string) map[string]string {
	if fw == "" || fw == DeclaredFrameworkReact || fw == DeclaredFrameworkVite || fw == DeclaredFrameworkNextJS {
		return env
	}
	out := make(map[string]string, len(env)+8)
	for k, v := range env {
		out[k] = v
	}

	for k, host := range env {
		if !strings.HasPrefix(k, "AUTODEVOPS_") || !strings.HasSuffix(k, "_HOST") || host == "" {
			continue
		}
		alias := strings.TrimSuffix(strings.TrimPrefix(k, "AUTODEVOPS_"), "_HOST")
		prefix := "AUTODEVOPS_" + alias + "_"

		port := env[prefix+"PORT"]
		user := env[prefix+"USER"]
		pass := env[prefix+"PASSWORD"]
		db := env[prefix+"DATABASE"]
		scheme := env[prefix+"SCHEME"]
		if scheme == "" {
			switch port {
			case "5432":
				scheme = "postgresql"
			case "3306":
				scheme = "mysql"
			case "27017":
				scheme = "mongodb"
			case "6379":
				scheme = "redis"
			}
		}

		switch fw {
		case DeclaredFrameworkLaravel, DeclaredFrameworkPHP:
			if out["DB_HOST"] != "" {
				continue
			}
			out["DB_HOST"] = host
			out["DB_PORT"] = port
			switch scheme {
			case "postgresql":
				out["DB_CONNECTION"] = "pgsql"
			case "mysql":
				out["DB_CONNECTION"] = "mysql"
			}
			if db != "" {
				out["DB_DATABASE"] = db
			}
			if user != "" {
				out["DB_USERNAME"] = user
			}
			if pass != "" {
				out["DB_PASSWORD"] = pass
			}
		case DeclaredFrameworkNestJS, DeclaredFrameworkDjango, DeclaredFrameworkRails, DeclaredFrameworkNode:
			if out["DATABASE_URL"] != "" {
				continue
			}
			if host == "" || user == "" || pass == "" {
				continue
			}
			dbPart := ""
			if db != "" {
				dbPart = "/" + db
			}
			out["DATABASE_URL"] = fmt.Sprintf("%s://%s:%s@%s:%s%s", scheme, user, pass, host, port, dbPart)
		}
	}
	return out
}

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
