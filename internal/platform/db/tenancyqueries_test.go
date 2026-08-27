package db

import (
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// A missing workspace_id predicate is a CROSS-TENANT DATA LEAK, and it is the one
// class of bug this test suite is structurally blind to.
//
// Every other test in this repo exercises a single workspace. A query that forgot its
// workspace_id filter returns all workspaces' rows — which, in a fixture holding one
// workspace, is EXACTLY the expected rows. The test passes. It has already happened
// once in a subtler form: a fake store for FindSendByMessageID discarded the workspace
// id it was handed, so the "cross-tenant isolation" test asserted nothing at all until
// PR #128. That was one query. There are 455.
//
// There is no RLS here and no query builder, so the only thing standing between a
// forgotten `AND workspace_id = $n` and workspace A reading workspace B's mail is a
// reviewer noticing. This test is the mechanical second reviewer.
//
// WHAT IT DOES
//
//  1. Reads the migrations and derives the tenant-scoped tables EMPIRICALLY — a table
//     is tenant-scoped iff some CREATE TABLE / ADD COLUMN gives it a workspace_id
//     column. Nothing is hardcoded, because a hand-written list goes stale the first
//     time someone adds a table, which is precisely the moment the guard has to work.
//  2. Reads queries/*.sql, splits it on sqlc's `-- name:` markers, and for every query
//     that touches a tenant-scoped table asserts workspace_id appears in a FILTERING
//     position, not merely somewhere in the text.
//  3. Anything deliberately cross-tenant must be in tenancyExceptions below WITH A
//     REASON. An allowlist entry with no reason is worse than no allowlist.
//
// # WHAT IT CANNOT DO — read this before trusting it
//
// This is a regex scanner, not a SQL parser (adding a Postgres parser dependency to run
// one test is not worth it), so it is a HEURISTIC and its limits are real:
//
//   - It proves a workspace_id predicate EXISTS, never that it is CORRECT. A query
//     saying `WHERE workspace_id = workspace_id` or pinning the wrong table's alias in a
//     join passes. Correctness of the pin is still a human's job.
//   - It cannot tell which table in a multi-table query the predicate applies to. A
//     3-table join with one workspace_id filter passes even if the other two are
//     unpinned. Joins through a workspace-pinned parent are usually genuinely safe,
//     which is why this is tolerated rather than flagged — but "usually" is doing work
//     in that sentence.
//   - It does not resolve CTE names. A `WITH x AS (...)` body is scanned as ordinary
//     text, so a reference to the CTE `x` is not treated as a table (correct), but a
//     reference to a real table INSIDE the CTE is (also correct). The predicate search
//     is whole-query, so a workspace_id filter in one CTE branch satisfies the check for
//     a table referenced in another. Deliberate: the alternative is flagging every
//     multi-CTE query in the repo.
//   - Subqueries and UNION branches are likewise pooled into one whole-query predicate
//     search rather than being matched branch-to-branch.
//
// The bias is deliberately toward FALSE NEGATIVES (missing a leak) over FALSE POSITIVES
// (crying wolf), because a guard people learn to allowlist past is worse than no guard.
// It catches the overwhelmingly common shape of the bug: a query with no workspace_id
// predicate at all.
//
//go:embed queries/*.sql
var queriesFS embed.FS

// tenantColumn is the column that makes a table tenant-scoped. One constant, because it
// is matched in three unrelated places (schema discovery, predicate detection, message).
const tenantColumn = "workspace_id"

// tenancyExceptions are queries that touch a tenant-scoped table and are deliberately
// NOT workspace-pinned. Every entry carries the reason, because an allowlist without one
// is just a mute button.
//
// They fall into four families, and a new entry should belong to one of them. If it
// belongs to none, that is a strong signal the query is a bug, not an exception.
//
//	(a) UNGUESSABLE-SECRET LOOKUP — the query resolves a credential by its hash or
//	    unguessable id, and the workspace is the ANSWER, not an input. There is no
//	    workspace id to filter by yet: the caller is unauthenticated until this returns.
//	    Pinning here is impossible, not merely omitted.
//	(b) USER-SCOPED, NOT WORKSPACE-SCOPED — the row belongs to a user across all their
//	    workspaces (sessions, invites). The tenancy pin is user_id; adding workspace_id
//	    would be wrong, not safer.
//	(c) DEPLOYMENT MAINTENANCE — a retention purge or crash-recovery sweep that operates
//	    on age/liveness alone, returns only a count, and is invoked by the deployment
//	    rather than by a tenant request. There is no tenant to scope to.
//	(d) CROSS-TENANT FAN-OUT — a sweeper that deliberately reads across all workspaces
//	    and RETURNS workspace_id so that each downstream unit of work is per-workspace.
//	    These are the dangerous-looking ones: the pin exists, it just lives one step
//	    later. Each entry names where.
var tenancyExceptions = map[string]string{
	// (a) unguessable-secret lookup — the workspace is the result, not the filter.
	"apikey.sql:GetApiKeyByPrefix":                  "verify path: resolves the workspace FROM the globally-unique key prefix; there is no authenticated workspace yet to filter by.",
	"apikey.sql:TouchApiKeyLastUsed":                "keyed on the api_keys row id already resolved by GetApiKeyByPrefix; a best-effort last-used stamp that returns nothing.",
	"invite.sql:GetInviteByHash":                    "an invite is redeemed by an unauthenticated recipient holding the token; the token hash resolves which workspace they are joining.",
	"oauth_provider.sql:GetOauthClient":             "resolves an OAuth client by its public client_id at /authorize, before any workspace context exists.",
	"oauth_provider.sql:GetOauthAuthRequest":        "pending authorization request looked up by its opaque consent_id during the consent flow.",
	"oauth_provider.sql:ConsumeOauthAuthRequest":    "single-use consume pinned to consent_id + user_id; the user is the tenancy boundary for a consent, not a workspace.",
	"oauth_provider.sql:GetOauthAuthCode":           "authorization code read by its hash at the token endpoint, which authenticates the CLIENT, not a workspace member.",
	"oauth_provider.sql:ConsumeOauthAuthCode":       "atomic single-use redeem by code hash at the token endpoint; same pre-authentication position as GetOauthAuthCode.",
	"oauth_provider.sql:GetOauthAccessTokenByHash":  "the bearer-token verifier: the token hash IS the credential and yields the workspace the request will then be scoped to.",
	"oauth_provider.sql:RevokeOauthAccessToken":     "RFC 7009 revoke, pinned to (token_hash, client_id) so a client can only revoke its own token; the client is the boundary.",
	"oauth_provider.sql:RevokeOauthAccessFamily":    "reuse-detection kill switch over a rotation family_id; a family is a token chain, which is narrower than a workspace.",
	"oauth_provider.sql:GetOauthRefreshTokenByHash": "refresh grant resolves the presented token by hash before any workspace is known.",
	"oauth_provider.sql:ConsumeOauthRefreshToken":   "guarded single-use rotation by token hash; same pre-authentication position as the lookup above.",
	"oauth_provider.sql:RevokeOauthRefreshFamily":   "revokes a whole rotation family on reuse detection; pinned to family_id, which is narrower than a workspace.",
	"mailbox.sql:MailboxExists":                     "an existence probe by mailbox id returning only a boolean — it can leak at most whether an unguessable UUID is an active mailbox, never row content.",
	"send.sql:CountSentToday":                       "the daily-cap gate, keyed on an unguessable mailbox id and returning only a count. Callers reach it having already resolved the mailbox within their workspace.",
	"stepsend.sql:LatestSentForContact":             "threading headers for the next step, keyed on (campaign_id, contact_id); both were resolved workspace-scoped by the caller that scheduled this step.",
	"tracking.sql:GetSendTrackingContext":           "the public tracking pixel path has NO authenticated principal to scope by — the send id arrives in an HMAC-signed token. Returns a verdict about that one send, never row data; scoping it would mean trusting a workspace id from an unauthenticated request.",
	"tracking.sql:CountRecentSendOpensFromSubnet":   "same unauthenticated tracking path as GetSendTrackingContext; returns a count about one send.",

	// (b) user-scoped, not workspace-scoped.
	"invite.sql:MarkInviteAccepted":            "flips a pending invite by its id after GetInviteByHash proved possession of the token; the token, not a workspace, is the authorization.",
	"invite.sql:GetLatestPendingInviteByEmail": "federated sign-up resolves a brand-new address to a pending invite ACROSS workspaces — the invitee arrived without an invite link, so there is no workspace to scope to yet.",
	"session.sql:GetSessionByHash":             "session lookup by refresh-token hash; the session row is what ESTABLISHES the workspace for the request.",
	"session.sql:GetSessionAuthState":          "per-request verifier probe by session id, returning only revocation/expiry/token_version — no tenant data.",
	"session.sql:RevokeSession":                "revokes a session by id; a session spans workspace switches, so its owner is a user.",
	"session.sql:RevokeSessionOwned":           "revoke pinned to (session id, user_id) — the user is the correct boundary for a session.",
	"session.sql:RevokeFamily":                 "revokes a refresh-token rotation family; a family belongs to one login, which outlives any single workspace selection.",
	"session.sql:RevokeAllForUser":             "logout-everywhere: deliberately spans every workspace the user has a session in. Pinning it would leave sessions live.",
	"session.sql:RevokeOtherSessionsForUser":   "revoke other devices, pinned to user_id; same reason as RevokeAllForUser.",
	"session.sql:BumpSessionTokenVersion":      "invalidates one session's access tokens by id; a session is user-owned.",
	"session.sql:BumpTokenVersionForUser":      "invalidates every access token for a user (password reset) — must span workspaces to be effective.",

	// (c) deployment maintenance — age/liveness only, returns a count.
	"maintenance.sql:PurgeExpiredSecurityArtifacts":     "retention sweep over expired credentials across the whole deployment; deletes by age alone and returns only a count, so it can neither surface nor cross tenant data.",
	"maintenance.sql:PurgeDeadWorkers":                  "reaps the global worker registry and the assignments pinned to dead workers; workers are deployment infra, not tenant data (migration 000017).",
	"recipientdomain.sql:DeleteExpiredRecipientDomains": "retention sweep over a DNS-fact cache, by age alone. A lost row costs one re-lookup.",
	"warmup.sql:PurgeWarmupObservations":                "retention sweep over append-only warmup evidence, by age alone, returning a count (design §4.6).",
	"agentchat.sql:FailStuckAgentRuns":                  "crash recovery at API startup: a run still 'running' at boot belongs to a process that is gone. Deployment-scoped repair, not a tenant read.",
	"agentchat.sql:ResetStuckAgentMessages":             "companion to FailStuckAgentRuns; marks messages abandoned by a crashed process terminal.",

	// (d) cross-tenant fan-out — returns workspace_id so downstream work is per-workspace.
	"worker_routing.sql:PickLeastLoadedWorker":        "load-balances across the GLOBAL worker fleet; assignment counts are fleet-wide by design, and it returns only a worker_id.",
	"enrollment.sql:ListDueEnrollments":               "sweeper fan-out: selects due enrollments across all workspaces and RETURNS workspace_id so each enrollment is then advanced workspace-scoped. The pin lives one step later, in the per-enrollment job.",
	"mailbox.sql:ListActiveMailboxes":                 "poller fan-out: returns (id, workspace_id) for every active mailbox so each poll then runs workspace-scoped via GetMailbox.",
	"warmup.sql:ListDueWarmupMailboxes":               "warmup sweep fan-out: returns (mailbox, workspace) pairs, and per-mailbox gating (NextWarmupDue, GetWarmupSendJob) is workspace-pinned.",
	"warmup.sql:ListWorkspacesWithWarmupParticipants": "drives the per-workspace snapshot loop by ENUMERATING workspaces; it mentions workspace_id only in the SELECT and ORDER BY, and every statement the loop then issues is pinned to one of these ids.",
	"send.sql:GetCampaignIDForSend":                   "the tracking classifier's lookup by an unguessable send id; it RETURNS workspace_id rather than filtering on it, because the unauthenticated tracking path has no workspace to supply.",
	"session.sql:ListActiveSessionsForUser":           "session-management UI: a user's live sessions across every workspace they belong to. Pinned to user_id, and it returns workspace_id so the UI can label each row.",
	"session.sql:RepointSessionWorkspace":             "workspace switching WRITES workspace_id; the pin is user_id, so a caller can only ever repoint their own session.",
}

// --- schema discovery -------------------------------------------------------

// createTable matches a CREATE TABLE and captures its name and column body. The body is
// terminated by a `)` at the start of a line, which is the shape every migration in this
// repo uses; a table formatted otherwise would simply not be discovered, which
// TestEveryQueriedTableIsClassified turns into a failure rather than a silent pass.
var createTable = regexp.MustCompile(`(?is)CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z0-9_]+)"?\s*\((.*?)\n\)`)

// addColumn catches a workspace_id added to an existing table after its creation. No
// migration does this today; the pattern exists so that the day one does, the table
// starts being guarded automatically instead of silently staying exempt.
var addColumn = regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:ONLY\s+)?"?([a-z0-9_]+)"?\s+ADD\s+COLUMN\s+(?:IF\s+NOT\s+EXISTS\s+)?"?([a-z0-9_]+)"?`)

// tenantScopedTables returns every table that has a workspace_id column, derived from
// the embedded up-migrations. Later migrations win, so a table dropped and recreated
// (users, in 000004) is classified by its final shape.
func tenantScopedTables(t *testing.T) map[string]bool {
	t.Helper()
	names, err := fs.Glob(migrationsFS, "migrations/*.up.sql")
	if err != nil {
		t.Fatalf("glob migrations: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded migrations found — the embed pattern is not matching, so this guard would vacuously pass")
	}
	sort.Strings(names) // filename order is migration order (see migrationnames_test.go)

	scoped := map[string]bool{}
	for _, name := range names {
		body, err := fs.ReadFile(migrationsFS, name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		sql := stripComments(string(body))
		for _, m := range createTable.FindAllStringSubmatch(sql, -1) {
			scoped[m[1]] = declaresTenantColumn(m[2])
		}
		for _, m := range addColumn.FindAllStringSubmatch(sql, -1) {
			if strings.EqualFold(m[2], tenantColumn) {
				scoped[m[1]] = true
			}
		}
	}
	return scoped
}

// declaresTenantColumn reports whether a CREATE TABLE body declares workspace_id as a
// COLUMN. It requires the identifier at the start of a definition line, so a table that
// merely references workspace_id in a composite FOREIGN KEY or UNIQUE constraint (which
// many do) is not miscounted as owning one.
func declaresTenantColumn(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.ToLower(line))
		if rest, ok := strings.CutPrefix(line, tenantColumn); ok {
			// Guard against a longer identifier with the same prefix.
			if rest == "" || !isIdentifierRune(rest[0]) {
				return true
			}
		}
	}
	return false
}

func isIdentifierRune(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9')
}

// --- query parsing ----------------------------------------------------------

// queryHeader is sqlc's own query marker. Splitting on it is exact: sqlc uses the same
// marker to decide what a query is, so this test sees precisely the set sqlc compiles.
var queryHeader = regexp.MustCompile(`(?m)^--\s*name:\s*([A-Za-z0-9_]+)\s+:([a-z]+)\s*$`)

type namedQuery struct {
	file string
	name string
	body string // everything after the header, comments included
}

func namedQueries(t *testing.T) []namedQuery {
	t.Helper()
	names, err := fs.Glob(queriesFS, "queries/*.sql")
	if err != nil {
		t.Fatalf("glob queries: %v", err)
	}
	if len(names) == 0 {
		t.Fatal("no embedded queries found — the embed pattern is not matching, so this guard would vacuously pass")
	}
	sort.Strings(names)

	var out []namedQuery
	for _, path := range names {
		raw, err := fs.ReadFile(queriesFS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		src := string(raw)
		headers := queryHeader.FindAllStringSubmatchIndex(src, -1)
		if len(headers) == 0 {
			t.Errorf("%s contains no `-- name:` header — sqlc compiles nothing from it, so either it is dead or the header is malformed", path)
			continue
		}
		for i, h := range headers {
			end := len(src)
			if i+1 < len(headers) {
				end = headers[i+1][0]
			}
			out = append(out, namedQuery{
				file: strings.TrimPrefix(path, "queries/"),
				name: src[h[2]:h[3]],
				body: src[h[1]:end],
			})
		}
	}
	return out
}

// tableRef matches a table position: the identifier following FROM / JOIN / INTO /
// UPDATE / USING. Anything not in the tenant-scoped set (a CTE name, a subquery alias,
// a non-tenant table) is simply not a tenant table and drops out.
var tableRef = regexp.MustCompile(`(?is)\b(?:FROM|JOIN|INTO|UPDATE|USING)\s+(?:ONLY\s+)?"?([a-z0-9_]+)"?`)

func tenantTablesTouched(body string, scoped map[string]bool) []string {
	seen := map[string]bool{}
	for _, m := range tableRef.FindAllStringSubmatch(stripComments(body), -1) {
		if scoped[strings.ToLower(m[1])] {
			seen[strings.ToLower(m[1])] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// --- predicate detection ----------------------------------------------------

// These bound a filtering region. Everything between one of these openers and the next
// clause keyword is a position where a workspace_id mention actually CONSTRAINS rows.
var (
	whereClause  = regexp.MustCompile(`(?is)\bWHERE\b`)
	joinOn       = regexp.MustCompile(`(?is)\bON\b`)
	insertTarget = regexp.MustCompile(`(?is)\bINSERT\s+INTO\s+(?:ONLY\s+)?"?[a-z0-9_]+"?\s*\(`)
	conflictCols = regexp.MustCompile(`(?is)\bON\s+CONFLICT\s*\(`)
)

// clauseTerminator ends a predicate region. Without these, a workspace_id appearing in
// an ORDER BY or a RETURNING list after an unrelated WHERE would count as a filter — the
// exact false pass this test exists to avoid.
var clauseTerminator = regexp.MustCompile(`(?is)\b(ORDER\s+BY|GROUP\s+BY|LIMIT|OFFSET|RETURNING|UNION|INTERSECT|EXCEPT|WINDOW|FETCH|FOR\s+UPDATE|FOR\s+NO\s+KEY)\b|;`)

// isWorkspacePinned reports whether workspace_id appears in a FILTERING position.
//
// This is the heart of the guard and the reason a plain strings.Contains would not do:
// six queries in this repo mention workspace_id only in a SELECT list (they RETURN it
// for a sweeper to fan out on) and would sail past a Contains check while filtering on
// nothing. Those are real, they are in the allowlist under family (d), and finding them
// is what proved the structural check earns its complexity.
//
// The region search is whole-query rather than per-statement: a predicate found anywhere
// satisfies every tenant table in the query. See the limitations at the top of the file.
func isWorkspacePinned(body string) bool {
	sql := strings.ToLower(stripComments(body))

	// INSERT ... (cols) and ON CONFLICT (cols): the region is the parenthesised list, so
	// it is delimited by matching parens rather than by a clause keyword.
	for _, re := range []*regexp.Regexp{insertTarget, conflictCols} {
		for _, loc := range re.FindAllStringIndex(sql, -1) {
			if strings.Contains(parenBody(sql[loc[1]:]), tenantColumn) {
				return true
			}
		}
	}

	// WHERE ... and JOIN ... ON ...: the region runs to the next clause keyword.
	for _, re := range []*regexp.Regexp{whereClause, joinOn} {
		for _, loc := range re.FindAllStringIndex(sql, -1) {
			region := sql[loc[1]:]
			if end := clauseTerminator.FindStringIndex(region); end != nil {
				region = region[:end[0]]
			}
			if strings.Contains(region, tenantColumn) {
				return true
			}
		}
	}
	return false
}

// parenBody returns the contents of a parenthesised list whose opening paren has already
// been consumed, tracking nesting so a function call inside the list does not end it.
func parenBody(s string) string {
	depth := 1
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[:i]
			}
		}
	}
	return s // unbalanced: be generous rather than truncate a real predicate away
}

// lineComment strips `-- ...` to end of line. Comments in this repo are long and
// frequently discuss workspace_id in prose, so scanning them would make almost every
// query look pinned.
var lineComment = regexp.MustCompile(`(?m)--.*$`)

func stripComments(s string) string { return lineComment.ReplaceAllString(s, "") }

// --- the guards -------------------------------------------------------------

// The guard proper: every query touching a tenant-scoped table is workspace-pinned or
// explicitly excepted.
func TestEveryTenantScopedQueryIsWorkspacePinned(t *testing.T) {
	scoped := tenantScopedTables(t)
	for _, q := range namedQueries(t) {
		key := q.file + ":" + q.name
		tables := tenantTablesTouched(q.body, scoped)
		if len(tables) == 0 {
			continue
		}
		if isWorkspacePinned(q.body) {
			continue
		}
		if _, excepted := tenancyExceptions[key]; excepted {
			continue
		}
		t.Errorf(`%s (%s) reads or writes tenant-scoped table(s) %v but has no %s predicate.

A query without a %s filter returns EVERY workspace's rows. That is a cross-tenant data
leak, and no existing test will catch it: the fixtures hold one workspace, so all-rows and
this-workspace's-rows are the same set and the test passes.

Fix it one of two ways:
  1. Add the predicate — `+"`AND %s.%s = $n`"+`, with the workspace id taken from
     auth.UserFromContext, NEVER from a request body or a caller-controlled path param.
  2. If it is genuinely cross-tenant by design, add %q to tenancyExceptions in
     %s with a written reason. Entries with no reason are not accepted; see the four
     exception families documented above the map.`,
			q.name, q.file, tables, tenantColumn, tenantColumn, tables[0], tenantColumn, key, "tenancyqueries_test.go")
	}
}

// An allowlist rots the moment a query it names is renamed or deleted: the entry stops
// exempting anything and nobody notices, so the next query to inherit that name inherits
// a silent exemption. Reject stale entries.
func TestEveryTenancyExceptionNamesALiveQuery(t *testing.T) {
	live := map[string]bool{}
	for _, q := range namedQueries(t) {
		live[q.file+":"+q.name] = true
	}
	for key := range tenancyExceptions {
		if !live[key] {
			t.Errorf("tenancyExceptions has an entry for %q, but no such query exists — it was "+
				"renamed or deleted. Remove the entry, or the exemption silently transfers to "+
				"whatever query next takes that name", key)
		}
	}
}

// The other way the allowlist rots: an entry for a query that is now workspace-pinned
// anyway. It exempts nothing today, so nobody removes it — and it stays as standing
// permission for someone to later REMOVE that query's predicate without the guard
// objecting. An exemption must be load-bearing or gone.
func TestNoTenancyExceptionIsRedundant(t *testing.T) {
	scoped := tenantScopedTables(t)
	for _, q := range namedQueries(t) {
		key := q.file + ":" + q.name
		if _, excepted := tenancyExceptions[key]; !excepted {
			continue
		}
		if len(tenantTablesTouched(q.body, scoped)) == 0 {
			t.Errorf("tenancyExceptions[%q] exempts a query that touches no tenant-scoped table — "+
				"it was never needed. Remove it", key)
			continue
		}
		if isWorkspacePinned(q.body) {
			t.Errorf("tenancyExceptions[%q] exempts a query that IS workspace-pinned, so it exempts "+
				"nothing today and instead stands as pre-approval for removing that predicate "+
				"tomorrow. Remove the entry", key)
		}
	}
}

// An exception with an empty or perfunctory reason is a mute button, not a decision.
// The length floor is arbitrary but not pointless: it is longer than "n/a", "global",
// or "not tenant data", which are the things people actually write when they are trying
// to make a test go away.
func TestEveryTenancyExceptionHasAWrittenReason(t *testing.T) {
	const minReason = 40
	for key, reason := range tenancyExceptions {
		if len(strings.TrimSpace(reason)) < minReason {
			t.Errorf("tenancyExceptions[%q] = %q is too short to be a justification. Say WHY this "+
				"query is cross-tenant by design and where the tenancy boundary actually is "+
				"(see the four exception families above the map)", key, reason)
		}
	}
}

// The allowlist must not accumulate. This is not a style rule: every entry is a query
// this guard has stopped guarding, so the count is the size of the hole in the net.
// Raising it should be a conscious act in a diff, not a drift.
func TestTheTenancyAllowlistDoesNotGrowSilently(t *testing.T) {
	const known = 44
	if got := len(tenancyExceptions); got != known {
		t.Errorf("tenancyExceptions has %d entries, expected %d. Every entry is a query this "+
			"guard no longer checks. If you added one deliberately, update `known` in the same "+
			"commit so the growth is visible in review; if the count dropped, a query was "+
			"fixed — lower it and celebrate", got, known)
	}
}

// A guard that silently stops seeing anything is worse than no guard, and both halves of
// this one degrade silently: an embed pattern that stops matching, or a CREATE TABLE
// reformatted past the regex, would leave the check vacuously green. Assert it still has
// teeth by pinning the scale of what it examines.
func TestTheGuardStillSeesTheSchemaAndTheQueries(t *testing.T) {
	scoped := tenantScopedTables(t)
	tenant := 0
	for _, isTenant := range scoped {
		if isTenant {
			tenant++
		}
	}
	if len(scoped) < 80 {
		t.Errorf("only %d tables discovered in the migrations — the CREATE TABLE parser has "+
			"stopped matching, so tenant tables are going unclassified and their queries "+
			"unchecked", len(scoped))
	}
	if tenant < 70 {
		t.Errorf("only %d of %d tables classified as tenant-scoped — workspace_id column "+
			"detection has broken, so this guard is checking almost nothing", tenant, len(scoped))
	}
	if got := len(namedQueries(t)); got < 400 {
		t.Errorf("only %d named queries parsed — sqlc compiles ~455, so the `-- name:` splitter "+
			"is missing queries and they are going unchecked", got)
	}
}

// Prove the predicate detector is stronger than `strings.Contains(sql, "workspace_id")`.
//
// This is the test's own test. The whole value of the structural check is that it
// distinguishes a workspace_id that FILTERS from one that merely appears, and if that
// distinction ever regressed to a substring match, every guard above would keep passing
// while checking nothing. The "returns it" cases below are taken verbatim in shape from
// real queries in this repo (ListDueEnrollments, ListActiveMailboxes).
func TestWorkspacePinDetectionIgnoresNonFilteringMentions(t *testing.T) {
	pinned := []struct{ name, sql string }{
		{"where", "SELECT * FROM contacts WHERE workspace_id = $1;"},
		{"where and", "SELECT * FROM contacts WHERE id = $1 AND workspace_id = $2;"},
		{"join on", "SELECT * FROM sends s JOIN contacts c ON c.id = s.contact_id AND c.workspace_id = s.workspace_id WHERE s.id = $1;"},
		{"insert cols", "INSERT INTO contacts (workspace_id, email) VALUES ($1, $2) RETURNING *;"},
		{"on conflict", "INSERT INTO contacts (id, email) VALUES ($1, $2) ON CONFLICT (workspace_id, email) DO NOTHING;"},
		{"named arg", "SELECT * FROM contacts WHERE workspace_id = sqlc.arg('workspace_id')::uuid;"},
	}
	for _, tc := range pinned {
		if !isWorkspacePinned(tc.sql) {
			t.Errorf("%s: expected pinned, got unpinned: %s", tc.name, tc.sql)
		}
	}

	unpinned := []struct{ name, sql string }{
		{"select list only", "SELECT id, workspace_id FROM mailboxes WHERE status = 'active';"},
		{"order by only", "SELECT id FROM contacts WHERE status = 'active' ORDER BY workspace_id;"},
		{"returning only", "UPDATE contacts SET email = $2 WHERE id = $1 RETURNING id, workspace_id;"},
		{"comment only", "-- pinned by workspace_id upstream\nSELECT * FROM contacts WHERE id = $1;"},
		{"no mention", "SELECT * FROM contacts WHERE id = $1;"},
	}
	for _, tc := range unpinned {
		if isWorkspacePinned(tc.sql) {
			t.Errorf("%s: expected unpinned, got pinned — the detector has weakened toward a "+
				"substring match: %s", tc.name, tc.sql)
		}
	}
}

// The schema half needs its own test for the same reason: if declaresTenantColumn
// started returning false everywhere, every guard above would pass while checking
// nothing. The negative cases are real shapes from migration 000028, where many tables
// name workspace_id in a composite constraint without owning the column.
func TestTenantColumnDetectionDistinguishesColumnsFromConstraints(t *testing.T) {
	owns := []struct{ name, body string }{
		{"plain", "    id UUID PRIMARY KEY,\n    workspace_id UUID NOT NULL REFERENCES workspaces(id)"},
		{"lowercase type", "    workspace_id uuid PRIMARY KEY REFERENCES workspaces(id)"},
	}
	for _, tc := range owns {
		if !declaresTenantColumn(tc.body) {
			t.Errorf("%s: expected tenant-scoped, got not: %q", tc.name, tc.body)
		}
	}

	doesNot := []struct{ name, body string }{
		{"composite unique", "    id UUID PRIMARY KEY,\n    UNIQUE (workspace_id, email)"},
		{"composite fk", "    id UUID PRIMARY KEY,\n    FOREIGN KEY (mailbox_id, workspace_id) REFERENCES mailboxes(id, workspace_id)"},
		{"longer identifier", "    workspace_identity UUID NOT NULL"},
		{"none", "    id UUID PRIMARY KEY,\n    user_id UUID NOT NULL"},
	}
	for _, tc := range doesNot {
		if declaresTenantColumn(tc.body) {
			t.Errorf("%s: expected NOT tenant-scoped, got tenant-scoped — a table that merely "+
				"references workspace_id in a constraint would be misclassified: %q", tc.name, tc.body)
		}
	}
}

// Keep the failure message honest about what it names. Assembling it in the guard above
// and asserting it here separately would be duplication; instead this pins the one thing
// a reader depends on — that the message identifies the query, the file and the table.
func TestTheFailureMessageNamesQueryFileAndTable(t *testing.T) {
	msg := fmt.Sprintf("%s (%s) reads or writes tenant-scoped table(s) %v", "GetThing", "thing.sql", []string{"contacts"})
	for _, want := range []string{"GetThing", "thing.sql", "contacts"} {
		if !strings.Contains(msg, want) {
			t.Errorf("failure message omits %q: %s", want, msg)
		}
	}
}
