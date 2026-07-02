package akane

import "regexp"

// guardrailRE is a backstop — the persona prompt is the primary filter.
// Word-boundary anchors prevent false positives (e.g. "tit" in "titin").
// Prefix terms (masturbat, ejacul, etc.) use \b only at the start so
// conjugations like "masturbating" are still caught.
var guardrailRE = regexp.MustCompile(
	`(?i)(` +
		// sexual — exact words
		`\bsex\b|\bporn\b|\bnudes?\b|\bnaked\b|\bhorny\b|\bdick\b|\bcock\b|` +
		`\bpuss(y|ies)\b|\bvagina\b|\bpenis\b|\borgasm\b|\bboner\b|\bboobs?\b|\btits?\b|` +
		`\bnsfw\b|\bonlyfans\b|` +
		// sexual — prefix match (catch conjugations/derivations)
		`\bmasturbat|\bcumm|\bejacul|\berect(ion|ed|ing)?\b|` +
		// slurs
		`\bnigger\b|\bnigga\b|\bfaggot\b|\bretard\b|\btrann|\bspic\b|\bkike\b|\bchink\b` +
		`)`,
)

// passesGuardrail returns true if text contains no deny-list terms.
func passesGuardrail(text string) bool {
	return !guardrailRE.MatchString(text)
}
