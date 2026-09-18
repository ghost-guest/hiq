package secret

// Wire-format crypto constants.
//
// These strings feed KEK derivation (argon2id / HMAC salts) and OS-keystore
// lookups. They are part of the on-disk / on-keyring format, NOT branding:
// rotating a salt would make every existing secrets file undecryptable, and
// renaming the keyring service would orphan its entry. They therefore keep
// their pre-rebrand (fairpeer) spelling on purpose — the rebrand to hiq changed
// every *visible* name but must not touch key material.
//
// None of these are reachable on Windows: there the KEK is a random value
// DPAPI-wrapped inside the secrets file itself (see kek_windows.go), so the
// rebrand cannot rotate it. They matter on Linux/macOS and for the legacy v1
// format, which both must keep working across the rename.
const (
	// kekSalt prefixes the v2 KEK salt on non-Windows hosts. Used by the
	// passphrase-derived KEK and the degraded machine-bound KEK.
	kekSalt = "fairpeer-kek-v2:"

	// legacySecretSalt prefixes the v1 machine-key derivation (non-Windows).
	// Survives only to decrypt pre-v2 files during the one-time upgrade.
	legacySecretSalt = "fairpeer-secret-v1:"
)
