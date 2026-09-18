// Package portable exports and imports fairpeer's shareable assets — skills and
// memory documents — as a single zip bundle, so they survive a machine change
// and can be handed to a teammate.
//
// # Why this exists
//
// fairpeer ships as one self-contained portable exe: copy the folder and it
// runs. That promise stopped at the data boundary — skills and memory live in
// directories the user has to find and copy by hand, and a partial copy
// (or a copy that drags along a stale config.toml) is a support case waiting to
// happen. This package makes "take my setup with me" an explicit, checked
// operation.
//
// Counterpart in openhanako: character-cards/service.ts (an "agent is a folder"
// card whose export writes an explicit whitelist of keys rather than the whole
// agent record). Independent Go implementation of the same idea, with two
// deliberate differences noted below.
//
// # What a bundle carries
//
// A bundle is a zip whose root holds fairpeer-bundle.json plus the assets it
// lists. Kinds are skill (one skill), skills (a group) and memory (memory docs).
//
// # Two differences from the reference implementation
//
//  1. A size ceiling. The reference has none, which makes a bundle a
//     denial-of-service vector for whoever imports it. Every file, every entry
//     count and the uncompressed total are capped here.
//  2. An integrity check. Every entry records its SHA-256 in the manifest and
//     the import verifies it, so a truncated download or a hand-edited zip is
//     rejected instead of silently unpacking.
//
// # The invariant
//
// A bundle NEVER contains secrets.enc.json, config.toml, provider credentials,
// session transcripts or crash artefacts. The rule is enforced twice — on the
// way out (the export walk only ever descends a whitelist) and on the way in (a
// denied name is refused even if the zip came from elsewhere) — because the
// two failures are different: exporting a secret leaks it, importing one
// plants it. fairpeer's keys are DPAPI-sealed to the machine and user, so a
// bundle that carried them would look like it worked and then fail, which is
// worse than an honest empty field.
package portable
