//go:build integration

package acp

import (
	"fmt"
	"os"
	"sync/atomic"
	"testing"

	"github.com/gastownhall/gascity/internal/runtime"
	"github.com/gastownhall/gascity/internal/runtime/runtimetest"
)

// TestACPDefaultConformance runs the full Provider conformance suite against
// the production default ACP constructor. The runtime registry reaches
// NewSeamBacked whenever a city path is absent, and that branch always keeps
// socket and metadata state in the shared os.TempDir()/gc-acp directory that
// the isolated NewSeamBackedWithDir proof deliberately does not exercise.
func TestACPDefaultConformance(t *testing.T) {
	var fixture acpConformanceFixture
	var counter int64

	runtimetest.RunProviderTests(t, func(caseT *testing.T) (runtime.Provider, runtime.Config, string) {
		return NewSeamBacked(Config{}), runtime.Config{
			Command: acpConformanceCommand(caseT, t, &fixture),
			WorkDir: caseT.TempDir(),
		}, fmt.Sprintf("%s-%d", acpDefaultConformancePrefix(), atomic.AddInt64(&counter, 1))
	})
}

// acpDefaultConformancePrefix returns a per-process session-name prefix for the
// default-constructor proof. The shared state directory is the property under
// test, so it cannot be isolated per run; scoping every session name to this
// process instead keeps concurrent runs of the package from colliding on
// counter-derived names inside that one directory.
func acpDefaultConformancePrefix() string {
	return fmt.Sprintf("gc-acp-default-%d", os.Getpid())
}
