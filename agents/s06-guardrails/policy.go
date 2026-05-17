package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// Policy is the data side of the guardrail — declarative configuration
// that the three Checker implementations consume. Mirrors the three modes
// in guardrails.md L51-L116:
//
//	AllowedTools     — strict allow-list. Tool must be present (value=true)
//	                   to even be considered.
//	AllowedPathGlobs — glob patterns for the "path" argument (if any).
//	                   Empty means: no path constraint. We support a tiny
//	                   subset of glob syntax — "*" matches anything within
//	                   a single segment, "**" matches across segments.
//	BlockedPatterns  — regex patterns for the deny-list checker. Matched
//	                   against the concatenation of string-valued args
//	                   (or the "command" arg when present).
//	ToolTiers        — per-tool risk tier for the tiered checker. Values:
//	                   "low" | "medium" | "high" | "critical". A "critical"
//	                   verdict causes the tiered checker to return
//	                   ErrNeedsApproval.
//
// A single Policy can drive all three checkers — they read different
// fields. Real harnesses often chain them (e.g. allow-list AND deny-list);
// this chapter ships them independently so the failure mode of each is
// visible in isolation.
type Policy struct {
	AllowedTools     map[string]bool   `json:"allowed_tools,omitempty"`
	AllowedPathGlobs []string          `json:"allowed_path_globs,omitempty"`
	BlockedPatterns  []string          `json:"blocked_patterns,omitempty"`
	ToolTiers        map[string]string `json:"tool_tiers,omitempty"`
}

// LoadPolicyFromJSON reads a JSON-encoded Policy from disk. Strict mode:
// unknown fields are rejected so a typo in production config doesn't
// silently widen the trust boundary. Empty fields are allowed — a Policy
// with no AllowedTools simply blocks every tool when fed to the allow-list
// checker (default-deny is the whole point of the strict mode).
func LoadPolicyFromJSON(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("policy: read %s: %w", path, err)
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("policy: decode %s: %w", path, err)
	}
	return &p, nil
}
