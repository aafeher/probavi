package adapter

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// Wire envelope (§3). Exactly one of SandboxCall (adapter→core) or OK
// (final response) is present in adapter output.
type envelope struct {
	Protocol    string          `json:"protocol"`
	RequestID   string          `json:"request_id"`
	Op          string          `json:"op,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	SandboxCall *sandboxCall    `json:"sandbox_call,omitempty"`
	OK          *bool           `json:"ok,omitempty"`
	Error       *Error          `json:"error,omitempty"`
}

type sandboxCall struct {
	CallID string          `json:"call_id"`
	Verb   string          `json:"verb"`
	Args   json.RawMessage `json:"args"`
}

type sandboxResult struct {
	CallID string `json:"call_id"`
	OK     bool   `json:"ok"`
	Value  any    `json:"value,omitempty"`
	Error  *Error `json:"error,omitempty"`
}

type sandboxResultEnvelope struct {
	Protocol      string        `json:"protocol"`
	RequestID     string        `json:"request_id"`
	SandboxResult sandboxResult `json:"sandbox_result"`
}

// ProbeResult is the probe response payload (§6.1).
type ProbeResult struct {
	Name             string       `json:"name"`
	AdapterVersion   string       `json:"adapter_version"`
	ProtocolVersions []string     `json:"protocol_versions"`
	Engine           Engine       `json:"engine"`
	Sources          []SourceKind `json:"sources"`
	SQLRunner        SQLRunner    `json:"sql_runner"`
	VerbsRequired    []string     `json:"verbs_required"`
}

// Engine identifies the database engine an adapter drives.
type Engine struct {
	Name string `json:"name"`
}

// SourceKind is one supported backup source kind with its capabilities.
type SourceKind struct {
	Kind         string       `json:"kind"`
	Capabilities Capabilities `json:"capabilities"`
}

// Capabilities flags optional adapter features per source kind.
type Capabilities struct {
	PITR bool `json:"pitr"`
}

// SQLRunner is the declarative check-execution template (§6.1) that lets
// the core run SQL checks without learning engine concepts.
type SQLRunner struct {
	Argv []string          `json:"argv"`
	Env  map[string]string `json:"env"`
}

// ProvisionRequest is the provision request payload (§6.2).
type ProvisionRequest struct {
	Source  ProvisionSource   `json:"source"`
	Sandbox SandboxInfo       `json:"sandbox"`
	Options map[string]string `json:"options"`
	PITR    *PITR             `json:"pitr,omitempty"`
}

// ProvisionSource describes the backup source handed to the adapter.
type ProvisionSource struct {
	Kind          string            `json:"kind"`
	Path          string            `json:"path"`
	Params        map[string]string `json:"params"`
	CredentialEnv []string          `json:"credential_env"`
}

// SandboxInfo carries the provider guarantees the adapter may rely on.
type SandboxInfo struct {
	ScratchDir string `json:"scratch_dir"`
}

// PITR requests point-in-time recovery (only sent when probe declared the
// capability).
type PITR struct {
	TargetTime string `json:"target_time"`
}

// ProvisionResult is the provision response payload (§6.2).
type ProvisionResult struct {
	Connection     Connection      `json:"connection"`
	SourceIdentity SourceIdentity  `json:"source_identity"`
	Timings        Timings         `json:"timings"`
	State          json.RawMessage `json:"state"`
}

// Connection describes reachability from inside the sandbox. PasswordEnv
// names an environment variable — never a value (§2.5).
type Connection struct {
	Scheme      string `json:"scheme"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Database    string `json:"database"`
	User        string `json:"user"`
	PasswordEnv string `json:"password_env,omitempty"`
}

// SourceIdentity feeds the evidence record's backup identity.
type SourceIdentity struct {
	Checksum  string  `json:"checksum"`
	SizeBytes int64   `json:"size_bytes"`
	CreatedAt *string `json:"created_at"`
}

// Timings are the adapter-measured phases in seconds (§7); the core
// converts to integer milliseconds for evidence.
type Timings struct {
	EngineReadySeconds float64 `json:"engine_ready_seconds"`
	TransferSeconds    float64 `json:"transfer_seconds"`
	RestoreSeconds     float64 `json:"restore_seconds"`
}

// HealthcheckResult is the healthcheck response payload (§6.3).
type HealthcheckResult struct {
	Healthy        bool    `json:"healthy"`
	LatencySeconds float64 `json:"latency_seconds"`
	Detail         string  `json:"detail"`
}

// TeardownResult is the teardown response payload (§6.4).
type TeardownResult struct {
	Released bool `json:"released"`
}

var checksumPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Probe runs the probe operation. Sandbox calls are forbidden during probe.
func (r *Runner) Probe(ctx context.Context) (*ProbeResult, error) {
	payload, err := r.do(ctx, "probe", struct{}{}, nil, nil)
	if err != nil {
		return nil, err
	}
	res := &ProbeResult{}
	if err := json.Unmarshal(payload, res); err != nil {
		return nil, crashf("probe payload: %v", err)
	}
	if res.Name == "" {
		return nil, crashf("probe payload: name is empty")
	}
	if !contains(res.ProtocolVersions, ProtocolVersion) {
		return nil, &Error{Code: "unsupported_protocol",
			Message: fmt.Sprintf("adapter speaks %v, core speaks %s", res.ProtocolVersions, ProtocolVersion)}
	}
	return res, nil
}

// Provision runs the provision operation, mediating sandbox verbs; put_file
// sources are restricted to the drill's configured backup source path.
func (r *Runner) Provision(ctx context.Context, req *ProvisionRequest, verbs SandboxVerbs) (*ProvisionResult, error) {
	if verbs == nil {
		return nil, fmt.Errorf("provision: sandbox verbs are required")
	}
	normalizeProvisionRequest(req)
	payload, err := r.do(ctx, "provision", req, verbs, sourceGuard(req.Source.Path))
	if err != nil {
		return nil, err
	}
	res := &ProvisionResult{}
	if err := json.Unmarshal(payload, res); err != nil {
		return nil, crashf("provision payload: %v", err)
	}
	return res, validateProvisionResult(res)
}

// Healthcheck runs the healthcheck operation.
func (r *Runner) Healthcheck(ctx context.Context, conn *Connection, state json.RawMessage, verbs SandboxVerbs) (*HealthcheckResult, error) {
	req := struct {
		Connection *Connection     `json:"connection"`
		State      json.RawMessage `json:"state"`
	}{conn, normalizeState(state)}
	payload, err := r.do(ctx, "healthcheck", req, verbs, nil)
	if err != nil {
		return nil, err
	}
	res := &HealthcheckResult{}
	if err := json.Unmarshal(payload, res); err != nil {
		return nil, crashf("healthcheck payload: %v", err)
	}
	return res, nil
}

// Teardown runs the teardown operation. It must be invoked after every
// provision outcome, including crashes; state may be empty (§6.4).
func (r *Runner) Teardown(ctx context.Context, state json.RawMessage, reason string, verbs SandboxVerbs) (*TeardownResult, error) {
	switch reason {
	case "completed", "failed", "timeout", "cancelled":
	default:
		return nil, fmt.Errorf("teardown: invalid reason %q", reason)
	}
	req := struct {
		State  json.RawMessage `json:"state"`
		Reason string          `json:"reason"`
	}{normalizeState(state), reason}
	payload, err := r.do(ctx, "teardown", req, verbs, nil)
	if err != nil {
		return nil, err
	}
	res := &TeardownResult{}
	if err := json.Unmarshal(payload, res); err != nil {
		return nil, crashf("teardown payload: %v", err)
	}
	return res, nil
}

func normalizeProvisionRequest(req *ProvisionRequest) {
	if req.Source.Params == nil {
		req.Source.Params = map[string]string{}
	}
	if req.Source.CredentialEnv == nil {
		req.Source.CredentialEnv = []string{}
	}
	if req.Options == nil {
		req.Options = map[string]string{}
	}
	if req.Sandbox.ScratchDir == "" {
		req.Sandbox.ScratchDir = "/tmp"
	}
}

func validateProvisionResult(res *ProvisionResult) error {
	if !checksumPattern.MatchString(res.SourceIdentity.Checksum) {
		return crashf("provision payload: source_identity.checksum %q is not a sha256 reference", res.SourceIdentity.Checksum)
	}
	if res.Timings.EngineReadySeconds < 0 || res.Timings.TransferSeconds < 0 || res.Timings.RestoreSeconds < 0 {
		return crashf("provision payload: negative timings")
	}
	if res.Connection.Scheme == "" {
		return crashf("provision payload: connection.scheme is empty")
	}
	return nil
}

func normalizeState(state json.RawMessage) json.RawMessage {
	if len(state) == 0 {
		return json.RawMessage("{}")
	}
	return state
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}
