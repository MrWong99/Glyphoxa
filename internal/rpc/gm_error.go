package rpc

import (
	"regexp"
)

// GM-facing error text.
//
// A sentinel from a lower package carries that package's name as a prefix
// ("storage: invalid tag: …", "llmbuild: tenant has no …") and sometimes an
// ADR reference — both right in a log line and noise on a GM's screen.
// [gmError] keeps the error's identity (errors.Is / errors.As still match
// through it, so a classifier further up is unaffected) and only rewrites the
// text the wire carries.

// sentinelPrefix matches a leading Go package qualifier: "storage: ".
var sentinelPrefix = regexp.MustCompile(`^[a-z][a-z0-9]*: `)

// adrSuffix matches a trailing design-record reference: " (ADR-0054)".
var adrSuffix = regexp.MustCompile(`\s*\(ADR-\d+\)$`)

// gmError wraps err so its message drops the package prefix and any trailing
// ADR reference, for use as the cause of a GM-facing connect.Error. The wrapped
// error is still reachable via Unwrap.
func gmError(err error) error {
	if err == nil {
		return nil
	}
	return &gmMessage{msg: gmText(err.Error()), err: err}
}

// gmText is the rewrite gmError applies.
func gmText(s string) string {
	return adrSuffix.ReplaceAllString(sentinelPrefix.ReplaceAllString(s, ""), "")
}

type gmMessage struct {
	msg string
	err error
}

func (e *gmMessage) Error() string { return e.msg }
func (e *gmMessage) Unwrap() error { return e.err }
