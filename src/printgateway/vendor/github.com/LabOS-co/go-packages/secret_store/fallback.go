package secret_store

import (
	"errors"
	"fmt"
	"os"
)

// ErrSecretNotFound marks a definite "the store answered, and the secret
// genuinely isn't there" - the only condition GetSecretStringWithFallback
// treats as a candidate for its fallback. Any other error (transport
// failure, auth failure, a malformed response) means the store is broken,
// not merely empty, and must never be silently papered over by falling back
// to a possibly-stale value.
var ErrSecretNotFound = errors.New("secret not found")

// SecretValueGetter is the one SecretStoreClient method GetSecretString and
// GetSecretStringWithFallback actually need. Any SecretStoreClient satisfies
// it implicitly, so real callers pass one with no change on their side; a
// caller's test double only has to implement this one method instead of
// SecretStoreClient's full set.
//
// Contract for GetSecretValue(path): return (nil, nil) for a path that
// definitively doesn't exist. For a path that does exist, either shape is
// accepted - the fields directly, or the fields nested one level under a
// "data" key (the KV v2 wrapper this package's v1 VaultClient returns
// as-is; a "data" key present but nil, as KV v2 returns for a soft-deleted
// secret version, is also treated as the path not existing).
type SecretValueGetter interface {
	GetSecretValue(path string) (map[string]any, error)
}

// Fallback supplies a substitute value when the store definitively doesn't
// have what GetSecretStringWithFallback asked for. EnvFallback covers the
// common case (an environment variable); implement Fallback yourself for
// anything else - a hardcoded default, a local file, another store entirely.
type Fallback interface {
	Lookup() (value string, ok bool)
}

// EnvFallback is a Fallback backed by a single environment variable.
type EnvFallback string

func (e EnvFallback) Lookup() (string, bool) {
	v := os.Getenv(string(e))
	return v, v != ""
}

// GetSecretString resolves a single string value at key within the secret
// at path, unwrapping the KV v2 response shape: a secret's fields sit one
// level under "data" (the sibling "metadata" key, carrying version/
// created_time info, is not part of what this returns) - GetSecretValue
// returns that wrapper as-is, so this is the one place the unwrap happens
// rather than every caller having to know about it. Returns an error
// wrapping ErrSecretNotFound when the path or key is definitively absent,
// distinct from a transport/auth/malformed-response error.
func GetSecretString(client SecretValueGetter, path, key string) (string, error) {
	if client == nil {
		return "", fmt.Errorf("nil secret store client")
	}

	raw, err := client.GetSecretValue(path)
	if err != nil {
		return "", err
	}
	if raw == nil {
		return "", fmt.Errorf("path %s: %w", path, ErrSecretNotFound)
	}

	fields, err := unwrapFields(raw, path)
	if err != nil {
		return "", err
	}

	value, ok := fields[key]
	if !ok {
		return "", fmt.Errorf("key %q at path %s: %w", key, path, ErrSecretNotFound)
	}

	str, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("key %q at path %s is not a string", key, path)
	}

	return str, nil
}

// unwrapFields extracts a secret's field map from a raw GetSecretValue
// response, handling three real shapes rather than assuming one:
//   - v1's KV v2 wrapper, fields nested under "data" (this package's
//     VaultClient.GetSecretValue returns exactly this, un-unwrapped);
//   - a soft-deleted secret version, which comes back as a PRESENT "data"
//     key whose value is nil (Vault still returns this with no transport
//     error) - correctly a miss, not a malformed response;
//   - secret_store/v2's GetSecretValue, which returns the fields directly
//     with no "data" wrapper at all - so this same function keeps working
//     unchanged if this package is ever built against that version.
func unwrapFields(raw map[string]any, path string) (map[string]any, error) {
	wrapped, hasDataKey := raw["data"]
	if hasDataKey && wrapped == nil {
		return nil, fmt.Errorf("path %s: %w", path, ErrSecretNotFound)
	}

	if fields, ok := wrapped.(map[string]any); ok {
		return fields, nil
	}
	if hasDataKey {
		return nil, fmt.Errorf("path %s: unexpected secret store response shape", path)
	}

	// No "data" key at all: raw is already the unwrapped field map.
	return raw, nil
}

// GetSecretStringWithFallback is GetSecretString, falling back to
// fallback.Lookup() only when the store definitively lacks the path or key.
// A transport, auth, or malformed-response error is returned as-is instead -
// it means the store is broken, not empty, and silently resolving a
// possibly-stale fallback value in that case would hide a real outage. A nil
// fallback is treated as "no fallback available", not a programming error.
func GetSecretStringWithFallback(client SecretValueGetter, path, key string, fallback Fallback) (string, error) {
	value, err := GetSecretString(client, path, key)
	if err == nil {
		return value, nil
	}

	if !errors.Is(err, ErrSecretNotFound) {
		return "", err
	}

	if fallback == nil {
		return "", err
	}

	fallbackValue, ok := fallback.Lookup()
	if !ok {
		return "", fmt.Errorf("%w and no fallback value is available", err)
	}

	return fallbackValue, nil
}
