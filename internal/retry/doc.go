// Package retry retries connection establishment with exponential backoff.
//
// It retries only the failures a later connection attempt can get past: class
// 08 connection exceptions, too_many_connections, cannot_connect_now, and
// network errors. Statement-level conditions such as serialization failures
// and deadlocks are not retried here; retrying a transaction is deploy.sql's
// decision.
package retry
