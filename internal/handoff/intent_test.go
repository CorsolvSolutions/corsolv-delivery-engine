package handoff

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// validIntent is the intent every negative test starts from, so each case
// changes exactly one thing and the failure it asserts is unambiguous.
func validIntent() Intent {
	return Intent{
		SchemaVersion: SchemaVersion,
		ProjectID:     "corsolv-managed-delivery-test",
		Repository: Repository{
			Slug:          "CorsolvSolutions/corsolv-managed-delivery-test",
			Origin:        "https://github.com/CorsolvSolutions/corsolv-managed-delivery-test.git",
			DefaultBranch: "main",
		},
		Checkout:  `D:\Development\corsolv-managed-delivery-test`,
		Objective: "Prove the portal can hand a greenfield project to the delivery engine.",
		Lifecycle: []string{"Plan", "Build", "Accept"},
		Acceptance: []Criterion{
			{ID: "ac-1", Statement: "A deterministic source change is merged into the default branch."},
		},
		Policy: Policy{
			NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: true,
		},
		RequestedBy: "portal",
		RequestedAt: time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
	}
}

func TestValidIntentIsAccepted(t *testing.T) {
	if err := validIntent().Validate(); err != nil {
		t.Fatalf("the baseline intent must validate, got: %v", err)
	}
}

// The guard this package exists for. Intent is a data contract; the moment any
// part of it can express a command, the portal has a remote shell.
func TestIntentCarriesNoExecutableContent(t *testing.T) {
	// Names that, on a struct reaching an execution layer, mean "run this".
	forbidden := []string{
		"argv", "cmd", "command", "commands", "script", "scripts",
		"shell", "exec", "run", "entrypoint", "hook", "hooks",
		"preStart", "postStart", "env", "environment",
	}

	var walk func(t reflect.Type, path string, depth int)
	seen := map[reflect.Type]bool{}
	walk = func(rt reflect.Type, path string, depth int) {
		if depth > 8 || seen[rt] {
			return
		}
		switch rt.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			walk(rt.Elem(), path, depth+1)
			return
		case reflect.Map:
			// A map is an open namespace: nothing here can prove a future key
			// is not a command. The contract stays closed.
			t.Errorf("%s is a map; the intent contract must be a closed set of named fields", path)
			return
		case reflect.Struct:
			seen[rt] = true
			for i := 0; i < rt.NumField(); i++ {
				f := rt.Field(i)
				for _, bad := range forbidden {
					if strings.EqualFold(f.Name, bad) {
						t.Errorf("%s.%s: the intent contract must not carry executable content", path, f.Name)
					}
				}
				walk(f.Type, path+"."+f.Name, depth+1)
			}
		}
	}
	walk(reflect.TypeOf(Intent{}), "Intent", 0)
}

func TestUnknownSchemaVersionIsRefusedNotCoerced(t *testing.T) {
	in := validIntent()
	in.SchemaVersion = SchemaVersion + 1
	err := in.Validate()
	if !errors.Is(err, ErrSchemaUnsupported) {
		t.Fatalf("a future schema must be refused as unsupported, got: %v", err)
	}
}

func TestDecodeRefusesUnknownFields(t *testing.T) {
	raw, err := json.Marshal(validIntent())
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	asMap["setupScript"] = "rm -rf /"
	withExtra, err := json.Marshal(asMap)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := DecodeIntent(withExtra); !errors.Is(err, ErrIntentInvalid) {
		t.Fatalf("an intent carrying an unknown field must be refused, got: %v", err)
	}
}

func TestDecodeAcceptsTheBaselineIntent(t *testing.T) {
	raw, err := json.Marshal(validIntent())
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeIntent(raw)
	if err != nil {
		t.Fatalf("decoding a valid intent: %v", err)
	}
	if got.ProjectID != validIntent().ProjectID {
		t.Fatalf("projectId round-trip: got %q", got.ProjectID)
	}
}

func TestIntentIsRefusedFailClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Intent)
		want   string
	}{
		{"empty project id", func(i *Intent) { i.ProjectID = "" }, "projectId"},
		{"project id with a path separator", func(i *Intent) { i.ProjectID = "a/b" }, "projectId"},
		{"project id with traversal", func(i *Intent) { i.ProjectID = ".." }, "projectId"},
		{"uppercase project id", func(i *Intent) { i.ProjectID = "Corsolv" }, "projectId"},
		{"slug is not owner/name", func(i *Intent) { i.Repository.Slug = "just-a-name" }, "repository.slug"},
		{"slug has three segments", func(i *Intent) { i.Repository.Slug = "a/b/c" }, "repository.slug"},
		{"no default branch", func(i *Intent) { i.Repository.DefaultBranch = "" }, "repository.defaultBranch"},
		{"no origin", func(i *Intent) { i.Repository.Origin = "" }, "repository.origin"},
		{
			"origin contradicts slug",
			func(i *Intent) { i.Repository.Origin = "https://github.com/SomeoneElse/other-repo.git" },
			"repository.origin",
		},
		{"no checkout", func(i *Intent) { i.Checkout = "" }, "checkout"},
		{"relative checkout", func(i *Intent) { i.Checkout = "./somewhere" }, "checkout"},
		{"no objective", func(i *Intent) { i.Objective = "" }, "objective"},
		{"no lifecycle", func(i *Intent) { i.Lifecycle = nil }, "lifecycle"},
		{"no acceptance", func(i *Intent) { i.Acceptance = nil }, "acceptance"},
		{
			"duplicate acceptance ids",
			func(i *Intent) {
				i.Acceptance = []Criterion{
					{ID: "ac-1", Statement: "one"},
					{ID: "ac-1", Statement: "two"},
				}
			},
			"duplicated",
		},
		{
			"acceptance with no statement",
			func(i *Intent) { i.Acceptance = []Criterion{{ID: "ac-1", Statement: "  "}} },
			"statement",
		},
		{
			"pull request without push",
			func(i *Intent) { i.Policy = Policy{NeedPR: true, NeedChecks: true, MergeHumanAction: "owner merges"} },
			"needPush",
		},
		{
			"merge without a pull request",
			func(i *Intent) { i.Policy = Policy{NeedPush: true, NeedChecks: true, NeedMerge: true} },
			"needPr",
		},
		{
			"merge without checks",
			func(i *Intent) { i.Policy = Policy{NeedPush: true, NeedPR: true, NeedMerge: true} },
			"needChecks",
		},
		{
			"merge withheld with no named human action",
			func(i *Intent) { i.Policy = Policy{NeedPush: true, NeedPR: true, NeedChecks: true} },
			"mergeHumanAction",
		},
		{"negative worker bound", func(i *Intent) { i.Policy.MaxWorkers = -1 }, "maxWorkers"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := validIntent()
			tc.mutate(&in)
			err := in.Validate()
			if err == nil {
				t.Fatalf("expected refusal mentioning %q, got none", tc.want)
			}
			if !errors.Is(err, ErrIntentInvalid) {
				t.Fatalf("expected ErrIntentInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected the refusal to mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// Withholding merge is a supported, and in fact the safer, posture. It must
// validate as long as the boundary is named.
func TestMergeMayBeWithheldWhenTheBoundaryIsNamed(t *testing.T) {
	in := validIntent()
	in.Policy = Policy{
		NeedPush: true, NeedPR: true, NeedChecks: true, NeedMerge: false,
		MergeHumanAction: "the delivery owner merges the PR after reading the evidence",
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("a named human merge boundary must be accepted, got: %v", err)
	}
}

func TestValidateReportsEveryProblemAtOnce(t *testing.T) {
	in := validIntent()
	in.ProjectID = ""
	in.Objective = ""
	in.Acceptance = nil

	err := in.Validate()
	if err == nil {
		t.Fatal("expected a refusal")
	}
	for _, want := range []string{"projectId", "objective", "acceptance"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected all problems at once; %q missing from: %v", want, err)
		}
	}
}

func TestSlugFromOrigin(t *testing.T) {
	cases := []struct {
		origin string
		want   string
		wantOK bool
	}{
		{"https://github.com/Corsolv/thing.git", "Corsolv/thing", true},
		{"https://github.com/Corsolv/thing", "Corsolv/thing", true},
		{"git@github.com:Corsolv/thing.git", "Corsolv/thing", true},
		{"https://github.com/Corsolv", "", false},
		{"not a url at all", "", false},
	}
	for _, tc := range cases {
		got, err := slugFromOrigin(tc.origin)
		if tc.wantOK && (err != nil || got != tc.want) {
			t.Errorf("slugFromOrigin(%q) = %q, %v; want %q", tc.origin, got, err, tc.want)
		}
		if !tc.wantOK && err == nil {
			t.Errorf("slugFromOrigin(%q) = %q; want an error", tc.origin, got)
		}
	}
}

// The delivery host is Linux and the portal that registers checkouts is
// Windows, so a Windows absolute path must survive the contract.
func TestWindowsCheckoutPathIsAbsolute(t *testing.T) {
	for _, p := range []string{`D:\Development\thing`, `C:/Users/thing`, `\\server\share\thing`} {
		if !isWindowsAbs(p) {
			t.Errorf("%q must be recognized as absolute", p)
		}
	}
	for _, p := range []string{`thing`, `./thing`, `D:thing`} {
		if isWindowsAbs(p) {
			t.Errorf("%q must not be recognized as absolute", p)
		}
	}
}
