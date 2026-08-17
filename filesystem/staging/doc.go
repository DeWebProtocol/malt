// Package staging provides a platform-neutral, crash-recoverable local dirty
// overlay for one caller-selected filesystem View. It durably records local
// intent before reporting success and never uploads data, computes a candidate
// root, or mutates accepted trust state.
package staging
