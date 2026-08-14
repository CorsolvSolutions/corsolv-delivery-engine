package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gastownhall/gascity/internal/handoff"
)

// hostFile is the on-disk form of the delivery host's profile.
//
// It is a file for the same reason the run spec is: every machine-specific
// fact this layer needs — where delivery state lives, which forge CLI is
// authenticated, how a Windows path maps onto this host — is declared rather
// than inferred. Moving managed delivery to another machine is a change to this
// file and to nothing else.
type hostFile struct {
	DeliveryRoot       string `toml:"deliveryRoot"`
	Driver             string `toml:"driver"`
	GitHubCommand      string `toml:"githubCommand"`
	Provider           string `toml:"provider"`
	WindowsMountPrefix string `toml:"windowsMountPrefix"`

	// PlannerCommand and PlannerArgs are the agent that turns a brief into
	// work packages.
	PlannerCommand string   `toml:"plannerCommand"`
	PlannerArgs    []string `toml:"plannerArgs"`
}

// defaultHostPath is where the profile lives when none is named.
func defaultHostPath() string {
	if p := strings.TrimSpace(os.Getenv("CORSOLV_DELIVERY_HOST")); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "delivery-host.toml"
	}
	return filepath.Join(home, ".corsolv", "delivery-host.toml")
}

// loadHost reads the host profile.
//
// A missing profile is an error rather than a set of defaults. Defaults here
// would be guesses about which repository checkout, which forge account and
// which agent runtime a delivery run may use — exactly the facts that must
// never be guessed.
func loadHost(path string) (handoff.HostProfile, handoff.Planner, error) {
	var f hostFile
	data, err := os.ReadFile(path) //nolint:gosec // an operator-supplied path
	if err != nil {
		return handoff.HostProfile{}, nil, fmt.Errorf(
			"reading the delivery host profile %q: %w — managed delivery will not guess "+
				"where this machine keeps its state, which forge CLI is authenticated, or which "+
				"agent runtime may be started; write the profile first, or point at one with -host",
			path, err)
	}
	if err := toml.Unmarshal(data, &f); err != nil {
		return handoff.HostProfile{}, nil, fmt.Errorf("parsing the delivery host profile %q: %w", path, err)
	}

	host := handoff.HostProfile{
		DeliveryRoot:       f.DeliveryRoot,
		Driver:             f.Driver,
		GitHubCommand:      f.GitHubCommand,
		Provider:           f.Provider,
		WindowsMountPrefix: f.WindowsMountPrefix,
	}
	if err := host.Validate(); err != nil {
		return handoff.HostProfile{}, nil, fmt.Errorf("delivery host profile %q: %w", path, err)
	}

	planner := handoff.Planner(handoff.AgentPlanner{
		Command: f.PlannerCommand,
		Args:    f.PlannerArgs,
	})
	return host, planner, nil
}
