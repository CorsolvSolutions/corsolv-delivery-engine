package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The driver is a shell script the engine shells out to, and it runs on Linux —
// under WSL on the Windows hosts that drive managed delivery, and in CI.
//
// Git's autocrlf is commonly on for a Windows checkout, and a script it rewrites
// to CRLF is not a script Linux can run: the kernel reads the shebang's trailing
// carriage return as part of the interpreter name. The whole failure is one line
// of stderr, in a log nobody reads until a run has already died:
//
//	env: 'bash\r': No such file or directory
//
// That is not hypothetical. A live delivery run reached `city-up`, exhausted its
// attempts and reported the project failed in ten seconds, on a clone whose only
// difference from a working one was `core.autocrlf`. The engine was correct
// throughout — it faithfully reported that the stage it started could not run.
//
// So the ending is pinned in .gitattributes rather than left to host
// configuration, and these tests check both halves of that: the rule is
// declared, and this checkout actually honors it.

// repoRoot walks up from the package directory to the directory holding go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolving the package directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// shellScriptsUnder walks the repository for *.sh files.
//
// It walks rather than asking git, deliberately. This test belongs in the fast
// unit lane, where it catches a bad checkout in half a second, and the lane's
// subprocess census is a budget the repository keeps on purpose — spending a
// process spawn on a question the filesystem can answer would push this file
// into the integration tag and out of the lane where it is useful.
//
// Vendored and generated trees are skipped: their line endings are not this
// repository's to decide.
func shellScriptsUnder(t *testing.T, root string) []string {
	t.Helper()
	skip := map[string]bool{".git": true, "node_modules": true, "vendor": true, "dist": true}

	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skip[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".sh") {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			paths = append(paths, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return paths
}

// The rule itself. A checkout that happens to be correct today proves nothing
// about the next clone on a differently configured host.
func TestGitattributesPinsShellScriptsToLF(t *testing.T) {
	root := repoRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatalf("reading .gitattributes: %v", err)
	}
	text := string(data)
	for _, rule := range []string{"*.sh text eol=lf", ".githooks/* text eol=lf"} {
		if !strings.Contains(text, rule) {
			t.Errorf("`.gitattributes` does not declare %q.\n"+
				"Without it a Windows clone with core.autocrlf=true checks the driver out with CRLF, "+
				"and every stage it starts dies with `env: 'bash\\r': No such file or directory`.", rule)
		}
	}
}

// And this checkout. The rule is only worth having if the files on disk obey it,
// and this is the assertion that turns a ten-second mystery run failure into a
// named defect.
func TestShellScriptsInThisCheckoutHaveNoCarriageReturns(t *testing.T) {
	root := repoRoot(t)
	scripts := shellScriptsUnder(t, root)
	if len(scripts) == 0 {
		t.Fatal("no shell scripts found; this test is not looking where it thinks it is")
	}

	var offenders []string
	for _, rel := range scripts {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("reading %s: %v", rel, err)
			continue
		}
		if bytes.Contains(data, []byte("\r\n")) {
			offenders = append(offenders, rel)
		}
	}

	if len(offenders) > 0 {
		t.Errorf("%d shell script(s) are checked out with CRLF and cannot run on Linux: %s\n"+
			"Fix this checkout with `git add --renormalize .` after confirming .gitattributes pins *.sh to LF, "+
			"or strip the carriage returns in place.",
			len(offenders), strings.Join(offenders, ", "))
	}
}
