package workspace

import (
	"path"
	"path/filepath"
	"strings"
)

// This file classifies names, not contents. Two questions come up at the tool
// layer that the policy alone cannot answer: is this a file whose contents are
// a credential, and is this a file that a later, ordinary command will execute?
// Both are properties of where a file sits and what it is called, and both are
// wanted by callers that have the name before they have anything else, so the
// answers live here beside the path handling and are exported for the tool
// layer to act on. Neither function touches the disk.

// credentialNames are files that hold a secret under a name everybody uses
// for it. Matched against the base name after lower-casing.
var credentialNames = map[string]bool{
	".netrc": true, "_netrc": true, ".npmrc": true, ".pypirc": true,
	".yarnrc": true, ".yarnrc.yml": true, ".pgpass": true, ".my.cnf": true,
	".htpasswd": true, ".git-credentials": true, ".docker-credentials": true,
	"credentials": true, "credentials.json": true,
	"id_rsa": true, "id_dsa": true, "id_ecdsa": true, "id_ed25519": true,
	"secrets.yml": true, "secrets.yaml": true, "secrets.json": true,
	"secrets.toml": true, "master.key": true, "service-account.json": true,
	"kubeconfig": true, "terraform.tfstate": true, "terraform.tfstate.backup": true,
	".terraformrc": true, "terraform.rc": true,
}

// credentialSuffixes are extensions that mark a key or a certificate's private
// half. .pem covers public certificates too, and so over-reports; the plan is
// to ask, not to guess which half a file is from its name.
var credentialSuffixes = []string{
	".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".ppk", ".asc", ".gpg", ".kdbx",
	".tfvars", ".tfvars.json",
}

// credentialDirs are directories whose every file is presumed to be a
// credential or a store of them, matched against any segment of the path.
var credentialDirs = map[string]bool{
	".ssh": true, ".aws": true, ".gnupg": true, ".azure": true, ".kube": true,
	".docker": true, ".config/gcloud": true, ".password-store": true,
}

// Credential reports whether name looks like a file that holds a secret: a
// dotenv file, a private key, a token store, or the configuration of a tool
// that keeps its credentials in it. It is a judgement on the name alone.
//
// It errs towards yes for the same reason shell.Destructive does. A model in
// agent mode reading .env unasked hands whatever is in it to a third party and
// to the autosaved transcript; asking first costs one keystroke, and a reader
// who wanted the read to happen answers it. .git/config is here because it
// carries remote URLs, which is where people paste tokens when they should
// not, and because nothing under .git is the model's to read in any case.
func Credential(name string) bool {
	rel := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))))
	base := path.Base(rel)

	if strings.HasPrefix(base, ".env") && base != ".env.example" && base != ".env.sample" && base != ".env.template" {
		return true
	}
	if credentialNames[base] {
		return true
	}
	if strings.HasPrefix(base, "id_") && !strings.HasSuffix(base, ".pub") {
		return true
	}
	for _, suffix := range credentialSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	if rel == ".git/config" || strings.HasSuffix(rel, "/.git/config") {
		return true
	}
	for dir := range credentialDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") || strings.Contains(rel, "/"+dir+"/") {
			return true
		}
	}
	return false
}

// hookNames are files that a build tool, package manager or shell reads and
// acts on without being asked to. Writing one plants code that the next
// `make`, `go test`, `npm install` or opened terminal will run, which is how
// an auto-approved write becomes an unapproved exec. Matched against the
// lower-cased base name.
var hookNames = map[string]bool{
	"makefile": true, "gnumakefile": true, "justfile": true, "rakefile": true,
	"taskfile.yml": true, "taskfile.yaml": true, "magefile.go": true,
	"package.json": true, "package-lock.json": true, "pnpm-lock.yaml": true,
	"yarn.lock": true, "bun.lockb": true, "bunfig.toml": true,
	"go.mod": true, "go.sum": true, "go.work": true, "go.work.sum": true,
	"cargo.toml": true, "cargo.lock": true, "build.rs": true,
	"pyproject.toml": true, "setup.py": true, "setup.cfg": true,
	"requirements.txt": true, "tox.ini": true, "noxfile.py": true,
	"conftest.py": true, "pipfile": true, "poetry.lock": true,
	"gemfile": true, "gemfile.lock": true, "brewfile": true,
	"build.gradle": true, "build.gradle.kts": true, "settings.gradle": true,
	"settings.gradle.kts": true, "pom.xml": true, "gradlew": true, "gradlew.bat": true,
	"cmakelists.txt": true, "meson.build": true, "configure": true, "configure.ac": true,
	"dockerfile": true, "containerfile": true, "compose.yml": true,
	"compose.yaml": true, "vagrantfile": true, "procfile": true,
	"tsconfig.json": true, "jsconfig.json": true, "deno.json": true, "deno.jsonc": true,
	"directory.build.props": true, "directory.build.targets": true,
	"global.json": true, "nuget.config": true, "profile.ps1": true,
	"microsoft.powershell_profile.ps1": true,
}

// hookSuffixes are extensions that mark a file as configuration a tool runs
// rather than data it reads. PowerShell scripts are here because a profile can
// be called anything so long as PowerShell is told to load it.
var hookSuffixes = []string{
	".mk", ".ps1", ".psm1", ".psd1", ".gradle", ".csproj", ".fsproj", ".vbproj",
	".sln", ".props", ".targets", ".nuspec", ".gemspec", ".cmake",
}

// hookPrefixes are base-name prefixes that mark a family of tool
// configuration: docker-compose.override.yml, webpack.config.prod.js,
// vite.config.ts, jest.config.mjs, and so on.
var hookPrefixes = []string{
	"docker-compose", "webpack.config", "vite.config", "vitest.config",
	"jest.config", "babel.config", "rollup.config", "esbuild.config",
	"eslint.config", "prettier.config", "postcss.config", "tailwind.config",
	"next.config", "nuxt.config", "svelte.config", "astro.config",
	"playwright.config", "cypress.config", "karma.conf", "gulpfile", "gruntfile",
}

// Hook reports whether writing name could plant code that a later ordinary
// command runs: a file under .git, anything in or beneath a dot-directory, a
// dotfile, or a build, manifest or tool-configuration file by name.
//
// The rule for dot-directories and dotfiles is deliberately blunt. .githooks,
// .vscode/tasks.json, .github/workflows, .envrc, .bashrc and .pre-commit-config.yaml
// are all places where a write becomes an exec, and enumerating them would
// leave whichever one the list forgot. A write to .gitignore costing a
// question under --yes is the price of that.
func Hook(name string) bool {
	rel := strings.ToLower(filepath.ToSlash(filepath.Clean(filepath.FromSlash(strings.TrimSpace(name)))))
	for _, segment := range strings.Split(rel, "/") {
		if strings.HasPrefix(segment, ".") && segment != "." && segment != ".." {
			return true
		}
	}

	base := path.Base(rel)
	if hookNames[base] {
		return true
	}
	for _, suffix := range hookSuffixes {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	for _, prefix := range hookPrefixes {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

// inSkippedDir reports whether rel, a cleaned relative name, sits under one of
// skipDirs. The root's own name is never judged, for the reason Search never
// prunes ".": a project directory called vendor is still the project.
func inSkippedDir(rel string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if skipDirs[segment] {
			return true
		}
	}
	return false
}

// inGit reports whether rel, a cleaned relative name, is the repository's
// own machinery. It is the one place a write is refused outright rather than
// confirmed: a hook or a rewritten config there runs as the user on the next
// commit, and no approval dialogue is a good enough reason to let a model
// author that.
func inGit(rel string) bool {
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == ".git" {
			return true
		}
	}
	return false
}
