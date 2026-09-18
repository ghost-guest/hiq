// Package pii masks credentials and personal identifiers before text is written
// to disk.
//
// # Why this exists
//
// fairpeer keeps durable notes about a project: doc memory, saved facts, and
// their MEMORY.md index. Those notes are written by a model summarising a
// conversation, and a conversation about an integration routinely contains the
// thing that makes the integration work — "the key is sk-live-…". The note is
// well-intentioned and the secret is now in a plain-text file, in a directory
// the user is encouraged to copy to another machine.
//
// The fix has to happen at the write, not at the read: once a secret is on
// disk, every later reader (a teammate, a bundle export, a backup) inherits it.
// So every memory write funnels through one helper that runs this package first.
//
// Counterpart in openhanako: lib/pii-guard.ts (scrubPII → {cleaned, detected},
// applied at the fact-store / pinned-memory / session-summary writes). This is
// an independent Go implementation of the same rules, with one deliberate
// difference: the number-shaped rules here verify their checksums (Luhn for
// cards, the GB 11643 check character for Chinese ID numbers) instead of
// matching a digit count. A note about an order number should not be mangled.
//
// # What it does not do
//
// It is a backstop, not a guarantee. A secret written in prose ("the password is
// hunter two") is not detectable by pattern, and this package will not pretend
// otherwise. It catches the shapes that actually leak: provider keys, PEM
// blocks, bearer tokens, JWTs, card numbers, Chinese ID numbers and US SSNs.
package pii

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Kind classifies what was found. The value travels into the mask so a reader
// of the scrubbed file knows what used to be there.
type Kind string

const (
	// KindProviderKey is a vendor-prefixed API key (sk-, AKIA, gsk_, ghp_, …).
	KindProviderKey Kind = "api-key"
	// KindPrivateKey is a PEM private-key block.
	KindPrivateKey Kind = "private-key"
	// KindInlineSecret is a key/secret/token/password assignment whose value
	// was high-entropy enough to be a credential rather than a placeholder.
	KindInlineSecret Kind = "inline-secret"
	// KindBearer is an Authorization: Bearer token.
	KindBearer Kind = "bearer-token"
	// KindJWT is a JSON Web Token (three base64url segments).
	KindJWT Kind = "jwt"
	// KindCardNumber is a payment card number that passes the Luhn check.
	KindCardNumber Kind = "card-number"
	// KindNationalID is a Chinese resident ID number with a valid check digit.
	KindNationalID Kind = "national-id"
	// KindSSN is a US Social Security number in a plausible range.
	KindSSN Kind = "ssn"
)

// Finding is one masked span. It deliberately carries a mask rather than the
// secret, so a caller can log findings without re-leaking what it just removed.
type Finding struct {
	Kind Kind
	// Mask is the token that replaced the span.
	Mask string
	// Line is the 1-based line the span started on.
	Line int
}

// maskOf renders the replacement token for a kind. The shape is deliberately
// greppable and free of the characters the memory index uses as delimiters
// (square brackets and parentheses), so a scrubbed hook line still parses.
func maskOf(k Kind) string { return "<redacted:" + string(k) + ">" }

// maskTokenRe matches an already-applied mask, so re-scrubbing a file does not
// nest masks.
var maskTokenRe = regexp.MustCompile(`<redacted:[a-z-]+>`)

var (
	// providerKeyRe covers the vendor prefixes that appear in real leaked text.
	providerKeyRe = regexp.MustCompile(`\b(?:` + strings.Join([]string{
		`sk-[A-Za-z0-9_-]{16,}`,
		`sk_live_[A-Za-z0-9]{16,}`,
		`sk_test_[A-Za-z0-9]{16,}`,
		`sk-ant-[A-Za-z0-9_-]{16,}`,
		`rk_live_[A-Za-z0-9]{16,}`,
		`AKIA[0-9A-Z]{16}`,
		`ASIA[0-9A-Z]{16}`,
		`gsk_[A-Za-z0-9]{20,}`,
		`ghp_[A-Za-z0-9]{20,}`,
		`gho_[A-Za-z0-9]{20,}`,
		`ghu_[A-Za-z0-9]{20,}`,
		`ghs_[A-Za-z0-9]{20,}`,
		`ghr_[A-Za-z0-9]{20,}`,
		`github_pat_[A-Za-z0-9_]{20,}`,
		`glpat-[A-Za-z0-9_-]{16,}`,
		`glc_[A-Za-z0-9_-]{20,}`,
		`xox[baprs]-[A-Za-z0-9-]{10,}`,
		`hf_[A-Za-z0-9]{20,}`,
		`npm_[A-Za-z0-9]{20,}`,
		`pypi-[A-Za-z0-9_-]{20,}`,
		`AIza[0-9A-Za-z_-]{30,}`,
		`ya29\.[A-Za-z0-9_-]{20,}`,
		// Twilio account SID + SendGrid are common in integration notes.
		`AC[0-9a-f]{32}`,
		`SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}`,
	}, "|") + `)\b`)

	// pemRe spans a whole PRIVATE KEY block, including its newlines.
	pemRe = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)

	// bearerRe catches the header form, which is how tokens usually appear in a
	// pasted request or a debug log.
	bearerRe = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9_\-\.=+/]{16,}`)

	// jwtRe is deliberately strict: three non-empty base64url segments, the
	// first of which decodes to a JSON header starting with {".
	jwtRe = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)

	// inlineSecretRe matches an assignment whose key names a credential. The
	// value is validated separately so `token: "${TOKEN}"` survives.
	inlineSecretRe = regexp.MustCompile(`(?i)\b(api[_-]?key|apikey|secret[_-]?key|client[_-]?secret|access[_-]?key|auth[_-]?token|access[_-]?token|refresh[_-]?token|private[_-]?key|passwd|password|passphrase)\b\s*[:=]\s*["']?([^\s"'` + "`" + `,;]{12,})["']?`)

	// cardRe needs a plausible shape before the checksum even runs: either four
	// separated groups, or a contiguous run starting with a real IIN.
	cardGroupedRe = regexp.MustCompile(`\b\d{4}(?:[ -]\d{4}){2,4}\b`)
	cardPlainRe   = regexp.MustCompile(`\b(?:4\d{12,18}|5[1-5]\d{14}|3[47]\d{13}|62\d{14,17})\b`)

	// idRe is the GB 11643 resident ID: 17 digits plus a check character.
	idRe = regexp.MustCompile(`\b\d{17}[\dXx]\b`)

	// ssnRe requires the separators; a bare nine-digit run is far too common.
	ssnRe = regexp.MustCompile(`\b\d{3}-\d{2}-\d{4}\b`)
)

// Scrub replaces every detected secret or identifier with a mask token and
// reports what it removed. The returned text is safe to write to disk.
//
// A text with nothing to hide is returned unchanged (the same string, so a
// caller comparing with == sees no change).
func Scrub(text string) (string, []Finding) {
	if text == "" {
		return text, nil
	}
	out := text
	var found []Finding

	replace := func(re *regexp.Regexp, kind Kind, keep func(match string) bool) {
		out = re.ReplaceAllStringFunc(out, func(m string) string {
			if keep != nil && !keep(m) {
				return m
			}
			mask := maskOf(kind)
			found = append(found, Finding{Kind: kind, Mask: mask, Line: lineOf(out, strings.Index(out, m))})
			return mask
		})
	}

	// Order matters only for readability of the report: the structured rules run
	// first, and the assignment rule runs last over whatever is left, so a value
	// a prefix rule already masked is never counted twice.
	replace(pemRe, KindPrivateKey, nil)
	replace(providerKeyRe, KindProviderKey, nil)
	replace(bearerRe, KindBearer, nil)
	replace(jwtRe, KindJWT, nil)
	replace(cardGroupedRe, KindCardNumber, isCardLike)
	replace(cardPlainRe, KindCardNumber, isCardLike)
	replace(idRe, KindNationalID, validChineseID)
	replace(ssnRe, KindSSN, validSSN)

	out = inlineSecretRe.ReplaceAllStringFunc(out, func(m string) string {
		sub := inlineSecretRe.FindStringSubmatch(m)
		if len(sub) < 3 || !looksLikeSecretValue(sub[1], sub[2]) {
			return m
		}
		mask := maskOf(KindInlineSecret)
		found = append(found, Finding{Kind: KindInlineSecret, Mask: mask, Line: lineOf(out, strings.Index(out, m))})
		return strings.Replace(m, sub[2], mask, 1)
	})

	if len(found) == 0 {
		return text, nil
	}
	return out, found
}

// Has reports whether text contains anything Scrub would mask.
func Has(text string) bool {
	_, found := Scrub(text)
	return len(found) > 0
}

// Masked reports whether text already carries a redaction mask, so a caller can
// tell the user that a note was altered as it was saved.
func Masked(text string) bool { return maskTokenRe.MatchString(text) }

// looksLikeSecretValue rejects the placeholders that dominate real config text,
// then decides whether the value has the character mix of a credential.
//
// The key name matters: `password` names a secret whatever its shape, so a
// lowercase passphrase is masked, while `api_key` is held to a stricter bar
// because a long lowercase word is far more often an identifier than a key.
func looksLikeSecretValue(key, v string) bool {
	if strings.ContainsAny(v, "${}<>") {
		return false
	}
	if strings.Contains(v, "…") || strings.Contains(v, "...") {
		return false
	}
	lower := strings.ToLower(v)
	for _, ph := range []string{"changeme", "change_me", "your-", "your_", "example",
		"placeholder", "redacted", "todo", "none", "null", "secret_here", "xxx"} {
		if strings.Contains(lower, ph) {
			return false
		}
	}
	k := strings.ToLower(key)
	if strings.Contains(k, "passw") || strings.Contains(k, "passphrase") ||
		strings.Contains(k, "private_key") {
		// A passphrase is a secret by definition: its shape carries no signal.
		return true
	}
	// A credential has to mix character classes; an all-lowercase word is prose.
	var digit, upper, symbol bool
	for _, r := range v {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
		default:
			symbol = true
		}
	}
	return digit || upper || symbol
}

// isCardLike strips separators and requires a Luhn-valid length.
func isCardLike(match string) bool {
	digits := digitsOnly(match)
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	return luhnValid(digits)
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// luhnValid is the card checksum. Requiring it is what keeps an order number
// out of the report: roughly nine in ten random digit runs fail.
func luhnValid(digits string) bool {
	sum, alt := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		n := int(digits[i] - '0')
		if alt {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		alt = !alt
	}
	return sum%10 == 0
}

// validChineseID checks the GB 11643 check character and the embedded birth
// date. Both are needed: the checksum alone still admits dates like 99999999.
func validChineseID(s string) bool {
	if len(s) != 18 {
		return false
	}
	// Embedded date: positions 6..13 as YYYYMMDD.
	year, _ := strconv.Atoi(s[6:10])
	month, _ := strconv.Atoi(s[10:12])
	day, _ := strconv.Atoi(s[12:14])
	if year < 1900 || year > 2100 || month < 1 || month > 12 || day < 1 || day > 31 {
		return false
	}
	weights := [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}
	const check = "10X98765432"
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(s[i]-'0') * weights[i]
	}
	want := check[sum%11]
	got := s[17]
	if got == 'x' {
		got = 'X'
	}
	return got == want
}

// validSSN applies the issuing rules that exclude the reserved ranges.
func validSSN(s string) bool {
	area, _ := strconv.Atoi(s[0:3])
	group, _ := strconv.Atoi(s[4:6])
	serial, _ := strconv.Atoi(s[7:11])
	if area == 0 || area == 666 || area >= 900 {
		return false
	}
	return group != 0 && serial != 0
}

// lineOf returns the 1-based line number of index i in s.
func lineOf(s string, i int) int {
	if i < 0 {
		return 0
	}
	return 1 + strings.Count(s[:i], "\n")
}

// Summary renders findings for a log line or a UI toast, e.g.
// "2 masked (api-key, national-id)".
func Summary(found []Finding) string {
	if len(found) == 0 {
		return ""
	}
	seen := map[Kind]bool{}
	var kinds []string
	for _, f := range found {
		if seen[f.Kind] {
			continue
		}
		seen[f.Kind] = true
		kinds = append(kinds, string(f.Kind))
	}
	return fmt.Sprintf("%d masked (%s)", len(found), strings.Join(kinds, ", "))
}
