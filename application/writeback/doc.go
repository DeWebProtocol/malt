// Package writeback orchestrates locally computed MALT client-root candidates
// from durable filesystem intent. It also completes a batch that a verified
// planner proves leaves the authenticated projection unchanged, without a
// remote mutation or candidate. It treats payload storage and materialization
// receipts as untrusted, records verified candidates separately, and has no
// accepted-root promotion capability.
package writeback
