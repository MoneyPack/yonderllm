package workspace

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/MoneyPack/yonderllm/internal/perm"
)

// Credential is a judgement on a name, so the judgement is tabled: the files a
// developer's tree most often keeps a secret in, and the neighbours that look
// similar but are not, so that a future change to the rules is caught as a
// changed verdict rather than as a reader's surprise.
func TestCredentialRecognisesSecretFilesByName(t *testing.T) {
	yes := []string{
		".env", ".env.local", ".env.production", "app/.env", "ENV/.Env",
		"id_rsa", "id_ed25519", ".ssh/id_rsa", "keys/id_ecdsa",
		"server.pem", "tls/server.key", "cert.p12", "cert.pfx", "store.jks",
		".netrc", "_netrc", ".npmrc", ".pypirc", ".pgpass", ".my.cnf",
		".aws/credentials", ".aws/config", "home/.aws/credentials",
		".git/config", "sub/.git/config",
		".gnupg/secring.gpg", ".kube/config", ".docker/config.json",
		"secrets.yml", "config/secrets.json", "config/master.key",
		"service-account.json", "terraform.tfstate", "prod.tfvars",
		".git-credentials", "credentials",
	}
	no := []string{
		"main.go", "README.md", "internal/perm/perm.go",
		".env.example", ".env.sample", ".env.template",
		"id_rsa.pub", "environment.go", "envelope.txt",
		"config.toml", "config.json", "package.json", "Makefile",
		"docs/keys.md", "keyboard.go", "pemdas.txt",
		".gitignore", ".github/workflows/ci.yml",
	}
	for _, name := range yes {
		if !Credential(name) {
			t.Errorf("Credential(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if Credential(name) {
			t.Errorf("Credential(%q) = true, want false", name)
		}
	}
}

// Hook is the same shape of judgement for writes: the files a later ordinary
// command will run without being asked, and the ordinary source files that
// must not cost a question under --yes.
func TestHookRecognisesFilesALaterCommandRuns(t *testing.T) {
	yes := []string{
		".git/hooks/pre-commit", ".git/config",
		".githooks/pre-commit", ".github/workflows/ci.yml", ".vscode/tasks.json",
		".envrc", ".bashrc", ".zshrc", ".profile", ".gitignore", "sub/.hidden/x",
		"Makefile", "GNUmakefile", "makefile", "build.mk", "rules/common.mk",
		"package.json", "package-lock.json", "go.mod", "go.sum", "go.work",
		"Cargo.toml", "build.rs", "pyproject.toml", "setup.py", "requirements.txt",
		"conftest.py", "Gemfile", "Rakefile", "build.gradle.kts", "pom.xml",
		"CMakeLists.txt", "Dockerfile", "docker-compose.yml", "docker-compose.override.yaml",
		"compose.yml", "Justfile", "Taskfile.yml", "magefile.go",
		"profile.ps1", "Microsoft.PowerShell_profile.ps1", "scripts/setup.ps1", "mod.psm1",
		"app.csproj", "app.sln", "Directory.Build.props", "tsconfig.json",
		"vite.config.ts", "jest.config.mjs", "webpack.config.prod.js", "eslint.config.js",
		"gulpfile.js", "Vagrantfile", "Procfile",
	}
	no := []string{
		"main.go", "internal/perm/perm.go", "README.md", "docs/SAFETY.md",
		"notes.txt", "src/index.ts", "lib/util.py", "cmd/app/main.go",
		"config.toml", "data.json", "schema.sql", "style.css",
		"makefile.md", "package.md", "go.mod.bak.txt",
	}
	for _, name := range yes {
		if !Hook(name) {
			t.Errorf("Hook(%q) = false, want true", name)
		}
	}
	for _, name := range no {
		if Hook(name) {
			t.Errorf("Hook(%q) = true, want false", name)
		}
	}
}

// The directories Search never enters are refused to ReadFile too. A model
// that cannot find .git/config by searching must not be able to name it and
// read it anyway, and the same rule applied to both keeps the two tools telling
// the same story about what the project is.
func TestReadFileRefusesTheDirectoriesSearchSkips(t *testing.T) {
	w, dir := open(t, perm.Agent)
	for name := range skipDirs {
		write(t, dir, name+"/inner.txt", "milk\n")
	}
	write(t, dir, "sub/.git/config", "[remote]\n")
	write(t, dir, "mine.txt", "milk\n")

	for name := range skipDirs {
		for _, path := range []string{name + "/inner.txt", "./" + name + "/inner.txt", "sub/../" + name + "/inner.txt"} {
			data, err := w.ReadFile(path)
			if err == nil {
				t.Errorf("ReadFile(%q) returned %d bytes, want a refusal", path, len(data))
				continue
			}
			if !errors.Is(err, errSkippedDir) {
				t.Errorf("ReadFile(%q) failed with %v, want errSkippedDir", path, err)
			}
		}
	}
	if _, err := w.ReadFile("sub/.git/config"); !errors.Is(err, errSkippedDir) {
		t.Errorf("ReadFile of a nested .git/config failed with %v, want errSkippedDir", err)
	}
	if _, err := w.ReadFile("mine.txt"); err != nil {
		t.Errorf("ReadFile of an ordinary file failed: %v", err)
	}
}

// A project directory that happens to be called vendor or build is still the
// project: only segments below the root are judged.
func TestReadFileDoesNotJudgeTheRootByItsName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "vendor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	write(t, dir, "mine.txt", "milk\n")

	w, err := Open(dir, perm.New(perm.Code))
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer w.Close()

	if _, err := w.ReadFile("mine.txt"); err != nil {
		t.Errorf("a root named vendor refused a read: %v", err)
	}
}

// A write under .git is refused however it is spelled and whatever the policy
// said, because a hook there runs as the user on the next commit. Nothing is
// created: not the file, and not the directory that would have held it.
func TestWriteFileRefusesAnythingUnderGit(t *testing.T) {
	for _, c := range []struct {
		name   string
		policy perm.Policy
	}{
		{"code", perm.New(perm.Code)},
		{"agent", perm.New(perm.Agent)},
		{"agent with auto-approval", perm.NewAutoApprove(perm.Agent)},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			w, err := Open(dir, c.policy)
			if err != nil {
				t.Fatalf("Open failed: %v", err)
			}
			defer w.Close()

			names := []string{
				".git/hooks/pre-commit",
				".git/config",
				"./.git/HEAD",
				"sub/.git/hooks/post-checkout",
				"sub/../.git/info/exclude",
			}
			if runtime.GOOS == "windows" {
				// A backslash is a separator only where the host says
				// so; elsewhere it is an odd character in a plain name.
				names = append(names, `.git\hooks\pre-push`)
			}
			for _, name := range names {
				err := w.WriteFile(name, []byte("#!/bin/sh\nrm -rf /\n"))
				if err == nil {
					t.Errorf("WriteFile(%q) was allowed, want a refusal", name)
					continue
				}
				if !errors.Is(err, errGitWrite) {
					t.Errorf("WriteFile(%q) failed with %v, want errGitWrite", name, err)
				}
				if !strings.HasPrefix(err.Error(), "workspace: write "+name+": ") {
					t.Errorf("WriteFile(%q) failed with %q, want it to name the file", name, err)
				}
			}
			if _, err := os.Stat(filepath.Join(dir, ".git")); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("a refused write left .git behind: %v", err)
			}
		})
	}
}

// .git is refused, but a dotfile or a manifest is not: those are the tool
// layer's to confirm, and refusing them here would make .gitignore and go.mod
// unwritable in every mode.
func TestWriteFileStillWritesDotfilesAndManifestsOutsideGit(t *testing.T) {
	w, dir := open(t, perm.Code)

	for _, name := range []string{".gitignore", ".github/workflows/ci.yml", "go.mod", "Makefile", ".gitmodules"} {
		if err := w.WriteFile(name, []byte("x\n")); err != nil {
			t.Errorf("WriteFile(%q) failed: %v", name, err)
			continue
		}
		wantFile(t, dir, name, "x\n")
	}
}

// A search under a context that is already done does no work and says why.
// The error wraps the context's own so a caller can tell an interrupted search
// from a broken one.
func TestSearchStopsBeforeStartingWhenCancelled(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a.txt", "milk\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	matches, err := w.Search(ctx, "milk")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Search under a cancelled context failed with %v, want context.Canceled", err)
	}
	if matches != nil {
		t.Errorf("Search returned %d matches alongside the cancellation", len(matches))
	}
}

// entry is the minimum fs.DirEntry the walker needs to make a decision about a
// regular file, so the mid-walk case can be driven by hand.
type entry struct{ name string }

func (e entry) Name() string               { return e.name }
func (e entry) IsDir() bool                { return false }
func (e entry) Type() fs.FileMode          { return 0 }
func (e entry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// The walk stops between entries, not only before the first. WalkDir has no
// seam for cancelling it part-way, so the callback is driven directly: a file
// is visited, the context is cancelled, and the very next visit must return
// the context's error rather than carry on reading.
func TestSearchStopsBetweenEntriesWhenCancelled(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a.txt", "milk\n")
	write(t, dir, "b.txt", "milk\n")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var matches []Match
	visit := w.searcher(ctx, "milk", &matches)

	if err := visit("a.txt", entry{"a.txt"}, nil); err != nil {
		t.Fatalf("the first visit failed: %v", err)
	}
	cancel()
	err := visit("b.txt", entry{"b.txt"}, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the visit after cancellation returned %v, want context.Canceled", err)
	}
	if len(matches) != 0 {
		// entry reports no Info, so the first visit read nothing; the point
		// is that the second added nothing either.
		t.Errorf("the walk kept collecting after cancellation: %d matches", len(matches))
	}
}

// The deadline that WalkDir sees is the deadline the caller set. A search
// under a context that expires part-way reports the deadline, which is how the
// tool layer tells the reader their timeout fired rather than the search
// misbehaving.
func TestSearchReportsADeadlineAsTheContextsError(t *testing.T) {
	w, dir := open(t, perm.Code)
	write(t, dir, "a.txt", "milk\n")

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	_, err := w.Search(ctx, "milk")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Search past its deadline failed with %v, want context.DeadlineExceeded", err)
	}
}

// A name rooted in Windows' spelling is refused for the reason it was never
// inside the workspace, on every platform. Before this the Unix build handed
// C:\Windows to the root as a relative name and reported a missing file.
func TestRelativeRefusesWindowsRootedNamesEverywhere(t *testing.T) {
	names := []string{
		`C:\Windows\System32\config\SAM`,
		`c:\temp\x.txt`,
		`C:notes.txt`,
		`\Windows\win.ini`,
		`\\server\share\x.txt`,
		`//server/share/x.txt`,
		"/etc/passwd",
	}
	for _, name := range names {
		rel, err := relative(name)
		if err == nil {
			t.Errorf("relative(%q) = %q, want a refusal", name, rel)
			continue
		}
		want := "workspace: " + name + " is outside the workspace"
		if err.Error() != want {
			t.Errorf("relative(%q) failed with %q, want %q", name, err, want)
		}
	}
}

// The check is for a drive letter, not for any colon: a file whose name has
// one further in is an ordinary name with an unusual character in it.
func TestRelativeKeepsNamesThatMerelyContainAColon(t *testing.T) {
	for _, name := range []string{"notes:draft.txt", "a/b:c.txt", "1:2"} {
		if _, err := relative(name); err != nil {
			t.Errorf("relative(%q) failed: %v, want it accepted", name, err)
		}
	}
}
