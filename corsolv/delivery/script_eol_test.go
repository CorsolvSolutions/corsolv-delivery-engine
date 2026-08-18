package main

import (
	"bytes"
	"os"
	"os/exec"
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
// declared, and this checkout actually honours it.

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

// trackedShellScripts lists the repository's tracked *.sh files, relative to the
// repository root. It reports false when git cannot answer — a source tarball
// with no .git is a legitimate place to run the suite, and failing there would
// be reporting the absence of git as a line-ending defect.
func trackedShellScripts(t *testing.T, root string) ([]string, bool) {
	t.Helper()
	cmd := exec.Command("git", "-C", root, "ls-files", "*.sh")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, false
	}
	var paths []string
	for _, line := range strings.Split(out.String(), "\n") {
		if p := strings.TrimSpace(line); p != "" {
			paths = append(paths, p)
		}
	}
	return paths, len(paths) > 0
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
	scripts, ok := trackedShellScripts(t, root)
	if !ok {
		t.Skip("git could not list tracked files; nothing to check in this checkout")
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
