<a name="1.4.0"></a>

# 1.4.0 (2026-08-26)

## New Features

- Added `GetSecretString`/`GetSecretStringWithFallback` (plain functions, no new type) for resolving a single string value at a path/key, unwrapping the KV v2 response shape. `GetSecretStringWithFallback` additionally falls back to a caller-supplied `Fallback` (e.g. the included `EnvFallback`, backed by an environment variable) - but only on a definite miss (`ErrSecretNotFound`); a transport/auth/malformed-response error is returned as-is rather than silently resolved to a possibly-stale fallback value. Both take a `SecretValueGetter` (the one `SecretStoreClient` method they need), so a caller's test double only has to implement one method.

<a name="1.3.0"></a>

# 1.3.0 (2024-05-09)

## New Features

- Added a new function (`GetDatabaseCredentials`) for getting database credentials from the secret store based on a role.
