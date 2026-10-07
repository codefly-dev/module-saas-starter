package infra

// The external policy log's transport, and why this deployment has none.
//
// SHAPE, from the infrastructure that provisions it: a DAY-partitioned
// `policy_log` table clustered on (operation_id, subject_id) with deletion
// protection, plus a `policy_log_replay` view that dedupes by ROW_NUMBER() over
// operation_id ordered by sequence. Writer and reader are distinct roles, and
// the reader deliberately holds no job-creation permission — so a replay is a
// bounded row read with a restriction, never a query.
//
// WHY NO CLIENT IS WIRED. Not for lack of a transport library: because that
// dataset cannot issue the RECEIPT the protocol is built on.
// `business.PolicyLog.Append` must return a token the LOG minted and a
// MONOTONIC SEQUENCE the LOG assigned, and the presence of those is the whole
// difference between "the log witnessed this narrowing" and "this host decided
// it". A BigQuery dataset gives a writer neither:
//
//   - the non-job write paths return no ordinal. `tabledata.insertAll` returns
//     only per-row errors, and the Storage Write API's offsets are monotonic
//     within one stream, so two replicas appending produce two independent
//     counters and no global order for the host's cursor to compare against.
//   - ingestion time, which the warehouse does assign, is not readable until
//     the streaming buffer flushes — minutes, against an append the protocol
//     bounds at five seconds, deliberately, so a slow log costs a refusal
//     rather than held locks.
//   - reading the row back to learn either is not available to the writer at
//     all: the roles are separate precisely so the appender cannot read, and
//     the reader holds no job-creation permission.
//
// So the missing piece is a RECEIPT-ISSUING APPENDER inside the warehouse's
// trust domain — the component that owns the writer credential, assigns the
// sequence and hands back the token — and that is infrastructure this service
// does not own. Until it exists, the host wires NO policy log (work.go), which
// means it serves normally and refuses every narrowing of authority. That is
// the protocol's own answer to a host that cannot witness a narrowing.
//
// WHAT MUST NOT GO HERE, and why this file is a comment rather than a type. The
// obvious way to make the four narrowing paths work again is a transport that
// returns a receipt it made up — any sequence, any token. That is the exact
// tautology the protocol exists to prevent: the host would record that the log
// witnessed the narrowing, nothing would have, and a restore of this database
// would silently reinstate the revoked authority with the receipt still sitting
// beside it as evidence that it had not. A client that cannot be exercised
// against the real service, in the service that owns authority, is worth less
// than an honest absence; a client that fabricates the one value the protocol
// checks is worth less than nothing.
//
// The previous occupant of this file, `UnavailablePolicyLog`, refused every
// append and every read. It was the right answer while nothing consulted the
// serving gate, and it became the wrong one the moment the gate reached the
// request path: a host holding a log it can never READ never refreshes
// `reached_at`, so the staleness window closes and the host stops answering
// anything at all. Refusing to narrow authority and refusing to serve are
// different refusals, and a deployment with no transport owes the first only.
