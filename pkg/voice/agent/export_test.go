package agent

// SpokenOutputRuleWithMarkup is the spoken-output paragraph as the system
// prompt renders it for a Voice that has an audio-markup instruction — exposed
// so the external prompt tests can pin the exact stable prefix.
const SpokenOutputRuleWithMarkup = spokenOutputRule + spokenOutputMarkupExemption

// SpokenOutputRule is the paragraph without the audio-tag exemption.
const SpokenOutputRule = spokenOutputRule

// ErrReplyTooLong exposes the spoken-text-cap generation error to its test.
var ErrReplyTooLong = errReplyTooLong
