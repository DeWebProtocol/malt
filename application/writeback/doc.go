// Package writeback orchestrates locally computed MALT client-root candidates
// from durable filesystem intent. It treats payload storage and materialization
// receipts as untrusted, records verified candidates separately, and has no
// accepted-root promotion capability.
package writeback
