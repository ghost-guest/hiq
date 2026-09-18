// Package netdev is the network-device operations capability (NETDEV_SPEC):
// connection transport (internal/netdev/transport), device inventory, and the
// credential plumbing. P0 ships the transport layer and the security seams
// (profile tool seal, secret namespace); drivers, tools, and UI land in P1+.
package netdev

import (
	"fmt"
	"strings"
	"time"

	"github.com/zzycxz/hiq/internal/secret"
)

// SecretNamespace is the key prefix under the secret store where every netdev
// credential lives: "netdev/<kind>/<name>" (e.g. "netdev/password/NETDEV_PWD_L1").
// Grouping under one prefix lets sandbox deny rules, audits, and future export
// tools address device credentials as a set, separate from provider keys and
// cowork mail passwords.
const SecretNamespace = "netdev"

// Secret kinds.
const (
	SecretKindPassword   = "password"   // device / hop login passwords
	SecretKindPassphrase = "passphrase" // private-key passphrases
	SecretKindSNMPAuth   = "snmp-auth"  // SNMPv3 auth passwords
	SecretKindSNMPPriv   = "snmp-priv"  // SNMPv3 privacy passwords
	SecretKindKubeconfig = "kubeconfig" // kind=k8s targets: the whole kubeconfig YAML
	SecretKindAPIToken   = "api-token"  // kind=firewall targets: vendor REST API tokens
)

// SecretKey builds the namespaced store key for one credential. name is the
// config-declared env-style identifier (the password_env value), so the key is
// stable across hosts that share a credential name.
func SecretKey(kind, name string) (string, error) {
	kind = strings.TrimSpace(kind)
	name = strings.TrimSpace(name)
	if kind == "" || name == "" {
		return "", fmt.Errorf("netdev: secret kind and name are required")
	}
	if strings.ContainsAny(kind+name, "/") {
		return "", fmt.Errorf("netdev: secret kind/name must not contain '/'")
	}
	return SecretNamespace + "/" + kind + "/" + name, nil
}

// SetSecret stores one netdev credential.
func SetSecret(kind, name, value string) error {
	key, err := SecretKey(kind, name)
	if err != nil {
		return err
	}
	return secret.Default().Set(key, value)
}

// GetSecret reads one netdev credential.
//
// netdev credentials are ALWAYS fetched explicitly through this accessor by
// the transport layer inside a netdev session — they are never loaded into the
// process environment (secret.Store.LoadIntoEnv), which coding/office bash
// children would inherit. That is the data-plane half of the profile
// isolation (NETDEV_SPEC §7.2).
// SecretHealth reports the netdev-namespace credential count and the
// vault file's last-change time (age granularity is the whole store — per-
// entry timestamps would need a schema bump; honest, coarse, cheap).
func SecretHealth() (count int, lastChanged time.Time) {
	keys, at, err := secret.Default().HealthStats()
	if err != nil {
		return 0, time.Time{}
	}
	n := 0
	for _, k := range keys {
		if strings.HasPrefix(k, SecretNamespace+"/") {
			n++
		}
	}
	return n, at
}

func GetSecret(kind, name string) (string, bool, error) {
	key, err := SecretKey(kind, name)
	if err != nil {
		return "", false, err
	}
	return secret.Default().Get(key)
}

// DeleteSecret removes one netdev credential.
func DeleteSecret(kind, name string) error {
	key, err := SecretKey(kind, name)
	if err != nil {
		return err
	}
	return secret.Default().Delete(key)
}
