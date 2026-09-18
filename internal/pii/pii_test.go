package pii

import (
	"strings"
	"testing"
)

// TestScrubMasksEveryRule pins each rule to a concrete sample, so a regex edit
// that silently stops matching fails here rather than in production.
func TestScrubMasksEveryRule(t *testing.T) {
	cases := []struct {
		name string
		text string
		kind Kind
	}{
		{"openai key", "key is sk-abcdefghijklmnopqrstuvwx", KindProviderKey},
		{"anthropic key", "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWx", KindProviderKey},
		{"aws access key", "AKIAIOSFODNN7EXAMPLE", KindProviderKey},
		{"google api key", "AIzaSyA1234567890abcdefghijklmnopqrstu", KindProviderKey},
		{"github pat", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", KindProviderKey},
		{"gitlab pat", "glpat-abcdefghijklmnopqrst", KindProviderKey},
		{"slack token", "xoxb-1234567890-abcdefghijkl", KindProviderKey},
		{"stripe live", "sk_live_abcdefghijklmnopqrstuvwx", KindProviderKey},
		{"npm token", "npm_abcdefghijklmnopqrstuvwxyz0123456789", KindProviderKey},
		{"bearer", "Authorization: Bearer abcdefghijklmnopqrstuvwx", KindBearer},
		{
			"jwt",
			"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
			KindJWT,
		},
		{"card grouped", "card 4111 1111 1111 1111 on file", KindCardNumber},
		{"card plain", "4111111111111111", KindCardNumber},
		{"chinese id", "身份证 11010519491231002X 已登记", KindNationalID},
		{"ssn", "ssn 123-45-6789", KindSSN},
		{"inline api key", `api_key = "Xk9mPq2Lr7TvBn4Ws8Yz"`, KindInlineSecret},
		{"inline secret", `client_secret: 9f8a7b6c5d4e3f2a1b0c`, KindInlineSecret},
		{"inline password", `password = "correcthorsebattery"`, KindInlineSecret},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, found := Scrub(tc.text)
			if len(found) == 0 {
				t.Fatalf("nothing was masked in %q", tc.text)
			}
			var kinds []string
			for _, f := range found {
				kinds = append(kinds, string(f.Kind))
			}
			if !contains(kinds, string(tc.kind)) {
				t.Fatalf("kinds = %v, want %q", kinds, tc.kind)
			}
			if !Masked(out) {
				t.Errorf("output carries no mask token: %q", out)
			}
			if out == tc.text {
				t.Errorf("output equals input: %q", out)
			}
		})
	}
}

// TestPEMBlockIsMaskedWhole checks the private-key rule spans the whole block:
// masking only the header line would leave the key material on disk.
func TestPEMBlockIsMaskedWhole(t *testing.T) {
	text := "config:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234\n5678abcd\n-----END RSA PRIVATE KEY-----\ndone\n"
	out, found := Scrub(text)
	if len(found) != 1 || found[0].Kind != KindPrivateKey {
		t.Fatalf("found = %+v, want one private-key finding", found)
	}
	if strings.Contains(out, "MIIEowIBAAKCAQEA1234") {
		t.Errorf("key material survived:\n%s", out)
	}
	if !strings.Contains(out, "config:") || !strings.Contains(out, "done") {
		t.Errorf("surrounding text should be untouched:\n%s", out)
	}
}

// TestScrubLeavesOrdinaryTextAlone is the false-positive guard. A memory file
// full of hashes, ids and numbers must survive a save unchanged.
func TestScrubLeavesOrdinaryTextAlone(t *testing.T) {
	texts := []string{
		"订单号 6217001234567890 已发货",
		"commit 3f2a9b1c4d5e6f7089abcdef1234567890abcdef",
		"接口返回 {\"code\":0,\"total\":123456}",
		"端口 8080，超时 30000 毫秒",
		"the password is hunter two",
		"api_key = \"${OPENAI_API_KEY}\"",
		"secret_key: <your-key-here>",
		"token = changeme",
		"# 项目记忆\n\n- 用 pnpm 而不是 npm\n",
		"身份证号格式为 18 位，最后一位可能是 X",
		"参考 RFC 4122 与 ISO 8601 标准",
		"版本 1.2.3，构建号 20260918",
	}
	for _, text := range texts {
		out, found := Scrub(text)
		if len(found) != 0 {
			t.Errorf("false positive on %q: %+v", text, found)
		}
		if out != text {
			t.Errorf("unchanged text must be returned as-is:\n in: %q\nout: %q", text, out)
		}
	}
}

// TestInvalidChecksumsAreNotMasked proves the checksum rules are doing work: a
// digit run of the right length but the wrong checksum is not a card or an ID.
func TestInvalidChecksumsAreNotMasked(t *testing.T) {
	if isCardLike("4111 1111 1111 1112") {
		t.Error("a card with a bad Luhn digit must not be treated as a card")
	}
	if validChineseID("110105194912310021") {
		t.Error("an ID with a bad check character must not be treated as an ID")
	}
	if validChineseID("11010599991231002X") {
		t.Error("an ID with an impossible birth date must not be treated as an ID")
	}
	if validSSN("000-12-3456") || validSSN("666-12-3456") || validSSN("900-12-3456") {
		t.Error("reserved SSN areas must not be treated as SSNs")
	}
}

// Re-scrubbing must not nest masks: a saved file is read back and saved again
// every time the panel writes it.
func TestScrubIsIdempotent(t *testing.T) {
	text := "key sk-abcdefghijklmnopqrstuvwx and card 4111111111111111"
	once, found := Scrub(text)
	if len(found) == 0 {
		t.Fatal("setup: nothing masked")
	}
	twice, again := Scrub(once)
	if again != nil {
		t.Errorf("a scrubbed text should yield no further findings, got %+v", again)
	}
	if twice != once {
		t.Errorf("re-scrubbing changed the text:\n%q\n%q", once, twice)
	}
}

// The report must be usable without re-leaking what it removed.
func TestFindingsCarryNoSecret(t *testing.T) {
	const secret = "sk-abcdefghijklmnopqrstuvwx"
	_, found := Scrub("token " + secret)
	if len(found) == 0 {
		t.Fatal("setup: nothing masked")
	}
	for _, f := range found {
		if strings.Contains(f.Mask, secret) {
			t.Errorf("a finding echoed the secret: %+v", f)
		}
	}
	if s := Summary(found); strings.Contains(s, secret) {
		t.Errorf("summary leaked the secret: %q", s)
	}
}

// Line numbers let a caller point at the offending line of a document.
func TestFindingsReportLineNumbers(t *testing.T) {
	text := "line one\nline two\nkey sk-abcdefghijklmnopqrstuvwx\n"
	_, found := Scrub(text)
	if len(found) != 1 {
		t.Fatalf("found = %+v", found)
	}
	if found[0].Line != 3 {
		t.Errorf("line = %d, want 3", found[0].Line)
	}
}

// Has is the cheap pre-check a caller uses to decide whether to warn the user.
func TestHas(t *testing.T) {
	if !Has("token sk-abcdefghijklmnopqrstuvwx") {
		t.Error("Has should report true for a key")
	}
	if Has("just a normal note about the build") {
		t.Error("Has should report false for ordinary text")
	}
}

// The mask shape must stay parseable by the memory index, whose row format uses
// square brackets and parentheses as delimiters.
func TestMaskAvoidsIndexDelimiters(t *testing.T) {
	out, _ := Scrub("api_key = \"Xk9mPq2Lr7TvBn4Ws8Yz\"")
	if strings.ContainsAny(out, "[]()\n|") {
		t.Errorf("mask introduces an index delimiter: %q", out)
	}
}

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
