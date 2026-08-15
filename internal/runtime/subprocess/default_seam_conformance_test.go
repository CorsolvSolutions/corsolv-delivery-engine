//go:build integration

package subprocess

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/runtimetest"
)

// TestSubprocessDefaultSeamConformance runs the full Provider conformance
// suite against the production default subprocess constructor. The runtime
// registry reaches NewSeamBacked whenever a city path is absent, and that
// branch keeps socket and metadata state in the shared os.TempDir()/gc-subprocess
// directory that the isolated NewSeamBackedWithDir proof deliberately does not
// exercise.
func TestSubprocessDefaultSeamConformance(t *testing.T) {
	var counter int64

	runtimetest.RunProviderTests(t, func(t *testing.T) (runtime.Provider, runtime.Config, string) {
		return NewSeamBacked(), runtime.Config{
			Command: "sleep 300",
			WorkDir: t.TempDir(),
		}, fmt.Sprintf("%s-%d", defaultSeamConformancePrefix(), atomic.AddInt64(&counter, 1))
	})
}

// defaultSeamConformancePrefix returns a per-process session-name prefix for
// the default-constructor proof. The shared state directory is the property
// under test, so it cannot be isolated per run; scoping every session name to
// this process instead keeps concurrent runs of the package from colliding on
// counter-derived names inside that one directory.
func defaultSeamConformancePrefix() string {
	return fmt.Sprintf("gc-subproc-default-%d", os.Getpid())
}
