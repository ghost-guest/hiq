package config

import (
	"testing"

	"github.com/BurntSushi/toml"
)

// TestNormalizeEmailAccountsFoldsLegacySingle verifies the single→multi
// migration: a config with only the legacy [cowork.smtp]/[cowork.imap] pair is
// folded into EmailAccounts[0] named "primary" and flagged Default, with the
// legacy fields kept in sync.
func TestNormalizeEmailAccountsFoldsLegacySingle(t *testing.T) {
	c := &CoworkConfig{}
	c.SMTP.Host = "smtp.139.com"
	c.SMTP.Port = 465
	c.IMAP.Host = "imap.139.com"
	c.IMAP.Port = 993

	normalizeEmailAccounts(c)

	if len(c.EmailAccounts) != 1 {
		t.Fatalf("expected 1 account after fold, got %d", len(c.EmailAccounts))
	}
	a := c.EmailAccounts[0]
	if a.Name != "primary" || !a.Default {
		t.Fatalf("expected primary default account, got name=%q default=%v", a.Name, a.Default)
	}
	if a.SMTP.Host != "smtp.139.com" || a.IMAP.Host != "imap.139.com" {
		t.Fatalf("legacy fields not folded into account: %+v", a)
	}
	// Legacy single fields stay in sync with the default account.
	if c.SMTP.Host != "smtp.139.com" || c.IMAP.Host != "imap.139.com" {
		t.Fatal("legacy SMTP/IMAP fields must stay in sync with default account")
	}
}

// TestNormalizeEmailAccountsPreservesMulti verifies that an explicit multi-account
// config is left intact, the first account becomes Default when none is flagged,
// and the legacy single fields mirror the default account.
func TestNormalizeEmailAccountsPreservesMulti(t *testing.T) {
	c := &CoworkConfig{
		EmailAccounts: []EmailAccount{
			{Name: "personal", SMTP: SMTPConfig{Host: "smtp.139.com", Port: 465}, IMAP: IMAPConfig{Host: "imap.139.com"}},
			{Name: "work", SMTP: SMTPConfig{Host: "smtp.mail.139.com"}, IMAP: IMAPConfig{Host: "imap.mail.139.com"}},
		},
	}

	normalizeEmailAccounts(c)

	if len(c.EmailAccounts) != 2 {
		t.Fatalf("expected 2 accounts preserved, got %d", len(c.EmailAccounts))
	}
	if !c.EmailAccounts[0].Default {
		t.Fatal("first account should become default when none is flagged")
	}
	if c.EmailAccounts[1].Default {
		t.Fatal("second account should not be default")
	}
	if c.SMTP.Host != "smtp.139.com" {
		t.Fatalf("legacy SMTP should mirror default account, got %q", c.SMTP.Host)
	}
}

// TestNormalizeEmailAccountsUniqueDefault verifies that multiple Default=true
// flags collapse to exactly one (the first), so the default account is deterministic.
func TestNormalizeEmailAccountsUniqueDefault(t *testing.T) {
	c := &CoworkConfig{
		EmailAccounts: []EmailAccount{
			{Name: "a", Default: true, SMTP: SMTPConfig{Host: "a"}},
			{Name: "b", Default: true, SMTP: SMTPConfig{Host: "b"}},
		},
	}

	normalizeEmailAccounts(c)

	defaults := 0
	for _, a := range c.EmailAccounts {
		if a.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("expected exactly 1 default, got %d", defaults)
	}
	if !c.EmailAccounts[0].Default {
		t.Fatal("expected the first default to be kept")
	}
}

// TestEmailAccountByName verifies lookup: empty name → default, case-insensitive
// name match, unknown name → ok=false.
func TestEmailAccountByName(t *testing.T) {
	c := CoworkConfig{
		EmailAccounts: []EmailAccount{
			{Name: "personal", Default: true},
			{Name: "work"},
		},
	}
	if a, ok := c.EmailAccountByName(""); !ok || a.Name != "personal" {
		t.Fatalf("empty name should return default, got %q ok=%v", a.Name, ok)
	}
	if a, ok := c.EmailAccountByName("WORK"); !ok || a.Name != "work" {
		t.Fatalf("case-insensitive lookup failed, got %q ok=%v", a.Name, ok)
	}
	if _, ok := c.EmailAccountByName("nope"); ok {
		t.Fatal("unknown name should return ok=false")
	}
}

// TestNormalizeEmailAccountsEmpty verifies that a config with no mail config at
// all stays empty (no synthetic account created).
func TestNormalizeEmailAccountsEmpty(t *testing.T) {
	c := &CoworkConfig{}
	normalizeEmailAccounts(c)
	if len(c.EmailAccounts) != 0 {
		t.Fatalf("expected 0 accounts when nothing configured, got %d", len(c.EmailAccounts))
	}
}

// TestEmailAccountsTOMLParse verifies the [[cowork.email_accounts]] TOML shape
// documented in hiq.example.toml / docs/email_setup.md actually decodes
// into the EmailAccounts slice with nested SMTP/IMAP intact.
func TestEmailAccountsTOMLParse(t *testing.T) {
	src := `
[[email_accounts]]
name    = "personal-139"
default = true
[email_accounts.smtp]
host            = "smtp.139.com"
port            = 465
from            = "a@139.com"
username        = "a@139.com"
password_env    = "MAIL_PWD_PERSONAL"
encryption_mode = "tls"
[email_accounts.imap]
host         = "imap.139.com"
port         = 993
username     = "a@139.com"
password_env = "MAIL_PWD_PERSONAL"

[[email_accounts]]
name = "work-cmcc"
[email_accounts.smtp]
host = "smtp.mail.139.com"
port = 465
[email_accounts.imap]
host = "imap.mail.139.com"
port = 993
`
	var cw CoworkConfig
	if err := toml.Unmarshal([]byte(src), &cw); err != nil {
		t.Fatalf("toml parse: %v", err)
	}
	if len(cw.EmailAccounts) != 2 {
		t.Fatalf("expected 2 accounts from TOML, got %d", len(cw.EmailAccounts))
	}
	if cw.EmailAccounts[0].Name != "personal-139" || !cw.EmailAccounts[0].Default {
		t.Fatalf("first account wrong: %+v", cw.EmailAccounts[0])
	}
	if cw.EmailAccounts[0].SMTP.Host != "smtp.139.com" || cw.EmailAccounts[0].SMTP.EncryptionMode != "tls" {
		t.Fatalf("nested SMTP not parsed: %+v", cw.EmailAccounts[0].SMTP)
	}
	if cw.EmailAccounts[1].IMAP.Host != "imap.mail.139.com" {
		t.Fatalf("second account IMAP host not parsed: %+v", cw.EmailAccounts[1].IMAP)
	}
}
