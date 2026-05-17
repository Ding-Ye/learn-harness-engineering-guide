package main

import (
	"errors"
	"io/fs"
	"net"
	"strings"
)

// ErrorClass is the recovery-strategy bucket an error falls into. The
// taxonomy mirrors upstream guide/error-handling.md L9-L18:
//
//	Transient  → network blip, rate limit, temporary 5xx. Retry with backoff.
//	Permanent  → file-not-found, permission denied, invalid input.   Report.
//	ModelError → malformed tool call, hallucinated name, bad JSON.   Re-prompt.
//	Resource   → out-of-memory, token limit, context-length blown.   Checkpoint + escalate.
//
// We add an explicit Unknown=0 zero value so a freshly-allocated
// ErrorClass (e.g. inside a struct literal) does not silently mean
// "Transient" — that would cause spurious retries and is the kind of
// bug the upstream Python sidesteps only because it uses string enums.
//
// Unknown is treated as Permanent for retry purposes (i.e., do not
// retry). This is the safe default: if we cannot recognise an error,
// we should not hammer the upstream service hoping it goes away.
type ErrorClass int

const (
	// Unknown means classify() could not match any signal. Do not retry.
	Unknown ErrorClass = iota
	// Transient is the only class RetryWithBackoff will retry.
	Transient
	// Permanent: file-not-found, validation failures, etc. Surface to caller.
	Permanent
	// ModelError: the model said something we cannot parse — re-prompt.
	ModelError
	// Resource: OOM, disk-full, token-budget blown — escalate, do not retry.
	Resource
)

// String makes ErrorClass printable for tests and CLI demo output.
func (c ErrorClass) String() string {
	switch c {
	case Unknown:
		return "Unknown"
	case Transient:
		return "Transient"
	case Permanent:
		return "Permanent"
	case ModelError:
		return "ModelError"
	case Resource:
		return "Resource"
	default:
		return "ErrorClass(?)"
	}
}

// transientSignals are substrings that, when found anywhere in
// err.Error(), classify the error as Transient. The set is a
// conservative superset of upstream L34-L38 plus a few real-world
// signals our HTTP clients in s02 actually surface ("connection
// reset", io.EOF on a half-closed socket, etc.).
var transientSignals = []string{
	"timeout",
	"rate limit",
	"429",
	"503",
	"502",
	"504",
	"connection reset",
	"connection refused",
	"temporary",
	"eof",
}

// resourceSignals indicate the agent has run out of something
// (memory, tokens, context). These are NOT retried; they bubble up to
// a checkpoint-and-escalate path. Upstream L51-L54.
var resourceSignals = []string{
	"out of memory",
	"disk full",
	"no space left",
	"token limit",
	"context length exceeded",
}

// modelSignals indicate the *model* produced garbage we cannot use.
// Recovery is a re-prompt, not a backoff. Upstream L43-L46.
var modelSignals = []string{
	"unknown tool",
	"invalid json",
	"missing required",
	"unexpected argument",
	"malformed",
	"schema validation",
}

// Classify returns the ErrorClass for err.
//
// The classifier is intentionally string-based on err.Error() because
// upstream errors come from many sources (net package, json.Unmarshal,
// the Anthropic HTTP API, our own tool code) and string-match is the
// one tool that works across all of them. Where Go gives us a stronger
// signal — net.Error.Timeout(), os.IsNotExist — we use it first.
//
// Precedence matters. We check the strongest typed signals before
// string matches; otherwise "no such file or directory: timeout.json"
// would be mis-classified as Transient just because the path contains
// the word "timeout".
//
//	1. nil           → Unknown (caller should not have called us)
//	2. net.Error.Timeout()=true → Transient
//	3. errors.Is(err, fs.ErrNotExist) → Permanent
//	4. resourceSignals → Resource    (check BEFORE transient: "token limit
//	                                  exceeded due to rate limit" is Resource)
//	5. modelSignals    → ModelError
//	6. transientSignals → Transient
//	7. default         → Unknown
func Classify(err error) ErrorClass {
	if err == nil {
		return Unknown
	}

	// 1. Typed network timeout — strongest signal we have. We use a
	//    type assertion rather than errors.As here because some net
	//    errors do not implement Unwrap; the explicit assertion makes
	//    the contract obvious. Callers wrapping with %w should also work
	//    because we fall through to the string match.
	if asErr, ok := err.(net.Error); ok && asErr.Timeout() {
		return Transient
	}

	// 2. fs.ErrNotExist covers wrapped %w errors AND raw os.ErrNotExist
	//    AND PathError from os.Open on a missing file. The older
	//    os.IsNotExist does not unwrap %w chains; errors.Is does.
	if errors.Is(err, fs.ErrNotExist) {
		return Permanent
	}

	msg := strings.ToLower(err.Error())

	// 3. Resource before Transient: a "rate limit" inside an OOM message
	//    should still be Resource (we need to free memory, not retry).
	for _, sig := range resourceSignals {
		if strings.Contains(msg, sig) {
			return Resource
		}
	}

	// 4. ModelError before Transient: "invalid json" can show up as part
	//    of a transient retry path, but the recovery is re-prompt, not sleep.
	for _, sig := range modelSignals {
		if strings.Contains(msg, sig) {
			return ModelError
		}
	}

	// 5. Transient signals.
	for _, sig := range transientSignals {
		if strings.Contains(msg, sig) {
			return Transient
		}
	}

	// 6. Default: Unknown. The retry layer treats this as "do not retry".
	return Unknown
}
