package sandbox_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/probavi/probavi/internal/sandbox"
)

func testDescriptor() sandbox.Descriptor {
	return sandbox.Descriptor{
		ID:   "demo",
		Name: "Demo",
		Params: []sandbox.Param{
			{Name: "image", Required: true, Doc: "Sandbox image."},
			{Name: "network", Default: "none", Doc: "Network to join."},
			{Name: "env.", Family: true, Doc: "Environment variable."},
		},
	}
}

func TestDescriptorLookup(t *testing.T) {
	d := testDescriptor()
	cases := []struct {
		name     string
		key      string
		wantOK   bool
		wantName string
	}{
		{name: "exact match", key: "image", wantOK: true, wantName: "image"},
		{name: "second exact match", key: "network", wantOK: true, wantName: "network"},
		{name: "family member", key: "env.POSTGRES_PASSWORD", wantOK: true, wantName: "env."},
		// A bare "env." reaches the provider so it can reject the empty
		// variable name with its own diagnostic, not as an unknown key.
		{name: "family prefix alone", key: "env.", wantOK: true, wantName: "env."},
		{name: "unknown key", key: "privileged", wantOK: false},
		{name: "near miss on a family", key: "environment.X", wantOK: false},
		{name: "empty key", key: "", wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, ok := d.Lookup(tc.key)
			if ok != tc.wantOK {
				t.Fatalf("Lookup(%q) ok = %v, want %v", tc.key, ok, tc.wantOK)
			}
			if ok && p.Name != tc.wantName {
				t.Errorf("Lookup(%q) resolved to %q, want %q", tc.key, p.Name, tc.wantName)
			}
		})
	}
}

func TestParamKey(t *testing.T) {
	cases := []struct {
		param sandbox.Param
		want  string
	}{
		{sandbox.Param{Name: "image"}, "image"},
		{sandbox.Param{Name: "env.", Family: true}, "env.<NAME>"},
	}
	for _, tc := range cases {
		if got := tc.param.Key(); got != tc.want {
			t.Errorf("Key() = %q, want %q", got, tc.want)
		}
	}
}

func TestParamKeys(t *testing.T) {
	got := testDescriptor().ParamKeys()
	want := []string{"image", "network", "env.<NAME>"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("ParamKeys() = %v, want %v", got, want)
	}
}

// TestUnknownParamError pins the diagnostic shape a typo produces: it must
// name every accepted key, because silently weakening a sandbox that holds
// production data is the failure this rejection exists to prevent.
func TestUnknownParamError(t *testing.T) {
	err := testDescriptor().UnknownParamError("privileged")
	if !errors.Is(err, sandbox.ErrInvalidParams) {
		t.Fatalf("error %v is not ErrInvalidParams", err)
	}
	for _, want := range []string{"demo", `"privileged"`, "image, network, env.<NAME>"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// TestUnhandledParamError covers the other direction: a key the descriptor
// declares but the provider never applies. A dropped parameter is a
// sandbox that is not what the drill asked for, so it fails rather than
// being ignored.
func TestUnhandledParamError(t *testing.T) {
	err := testDescriptor().UnhandledParamError("network")
	if !errors.Is(err, sandbox.ErrInvalidParams) {
		t.Fatalf("error %v is not ErrInvalidParams", err)
	}
	if !strings.Contains(err.Error(), "declared but not implemented") {
		t.Errorf("error %q does not explain the defect", err)
	}
}
