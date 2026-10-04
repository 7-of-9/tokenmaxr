# tokenmaxr: collector, server and dashboard spec (v1)

> This is the specification tokenmaxr grew from as d0m1.com's collector, and
> d0m1.com is still its reference server deployment: where it names d0m1.com
> pages (`/tokens`, `/collector`, `/collector/link`), those are that site's, not
> part of this repository. The wire protocol keeps its original names
> (`X-D0M1-Token`, `D0M1-` join codes) for compatibility. The GitHub publishing
> mode is described in the README.

This is the shared contract between the collector (Go), the API (Azure Static Web Apps managed Functions), and the site pages. Whoever changes a wire shape updates this file first.

## Goals

- `/tokens` (public; `/ai` and its sub-paths 301 to the `/tokens` equivalent, `/agents/prompts` to `/tokens/prompts`): a GitHub-style daily heatmap of AI token usage across every account and machine, with no account names.
- `/tokens/prompts` (private, owner role): every prompt with its date, provider, model, account label, workspace and machine.
- `/tokens/agents` (private, owner role; `/agents` redirects here): weekly account availability and reset times. The `/tokens` header has one GitHub sign-in action; authenticated users see Prompts, Agents and Sign out there. Every sign-in/out return preserves the current origin, path, query and fragment.
- `tokenmaxr` runs on every machine (Windows and macOS). You install it once and it works unattended. It fills downtime gaps automatically. Re-reading any source line, any number of times, on any machine, in any order, never changes the totals.

## Repository layout

```
collector/                 Go module d0m1.com/collector (Go 1.27, CGO_ENABLED=0)
  cmd/tokenmaxr/      main package (console subsystem everywhere; the Windows build also ships a -H windowsgui copy named tokenmaxrw.exe)
  cmd/signmanifest/        release tool: signs latest.json with ed25519
  internal/model/          wire types (THIS FILE's schemas), id helpers. Owned by the spec.
  internal/sources/{claude,codex,grok,cursor,gemini}/   parsers
  internal/homes/          scan roots: OS home, extraHomes, running WSL distros (v1.3)
  internal/...             state, outbox, upload, accounts, configfix, autostart, selfupdate, lock
api/                       SWA managed Functions, Node 22, @azure/functions v4, ESM JavaScript
  src/functions/*.js       enroll, invite, ingest, usage, prompts
  src/lib/*.js             tables, crypto, merge, rollup, auth
  test/                    node:test tests + local harness server
src/components/agents/     AgentsPage (heatmap), PromptsPage (private), LimitsPage (/tokens/agents, private), CollectorPage (install)
public/collector/          install.sh, install.ps1, uninstall.sh, uninstall.ps1, latest.json (+ .sig), added by release
.github/workflows/collector-release.yml
```

## Providers, sources, token semantics

`provider` is `anthropic` | `openai` | `xai` | `cursor` | `google`. `source` is `claude-code` | `codex` | `grok-cli` | `cursor` | `gemini-cli`, with `web-claude` | `web-chatgpt` | `web-grok` reserved for the future browser extension. Cursor is its own provider (its own subscription and its own model catalogue), and the underlying model is recorded in `model`.

Every usage event carries **disjoint** token buckets, so a sum never double counts:

| field | meaning |
|---|---|
| `in` | uncached input tokens (excludes cache reads and cache writes) |
| `cacheW` | cache-write / cache-creation input tokens |
| `cacheW1h` | the 1-hour-TTL part of `cacheW` (a subset, informational, never added; used only to price 1 h cache writes at their own rate) |
| `cacheR` | cache-read input tokens |
| `out` | output tokens, **including** reasoning |
| `reasoning` | reasoning tokens (a subset of `out`, informational only, never added) |
| `calls` | model calls represented (default 1) |

Mapping per source:

- **Claude Code** (`~/.claude/projects/**/*.jsonl`, recursive, because subagent transcripts nest to depth 6):
  - Consider assistant lines with `message.usage`.
  - Mapping: `in=input_tokens`, `cacheW=cache_creation_input_tokens`, `cacheW1h=cache_creation.ephemeral_1h_input_tokens` (0 when absent), `cacheR=cache_read_input_tokens`, `out=output_tokens`. Parser version **2** (v1.1 added `cacheW1h`; the bump makes every file reparse and the server replace rows).
  - The same `message.id` appears on several lines with growing `output_tokens`. Emit the fieldwise max seen so far; the server also merges by max.
  - Ignore `usage.iterations[]`.
  - nativeKey: `message.id`, falling back to `requestId`, falling back to `sessionId:uuid`.
  - model: `message.model`. Skip `<synthetic>` (zero usage).
- **Codex** (`$CODEX_HOME` or `~/.codex`, reading `sessions/**/rollout-*.jsonl` and `archived_sessions/**`):
  - `rolloutId` is the first `session_meta.payload.id` in the file. Never use `payload.session_id`, because it is shared across threads.
  - model: the most recent `turn_context.payload.model`.
  - **Mode rule (positional):** once a line with top-level `type:"token_usage_record"` has appeared in a file, ignore every later `event_msg/token_count` in that file. Earlier `token_count` lines still count.
    - `token_usage_record`: nativeKey is `response_id`. Use its usage fields with the same mapping as below.
    - legacy `event_msg` / `payload.type=token_count`:
      - Skip lines where `info == null`.
      - Count `info.last_token_usage` only when `info.total_token_usage.total_tokens` differs from the previous counted total in that file.
      - `epoch` starts at 0 and increments whenever the total *decreases* (a reset).
      - nativeKey is `rolloutId:epoch:total_tokens`.
  - Mapping: `cacheR=cached_input_tokens`, `cacheW=cache_write_input_tokens`, `in=max(0,input_tokens-cached_input_tokens-cache_write_input_tokens)`, `out=output_tokens`, `reasoning=reasoning_output_tokens`.
  - Resumed sessions append to old dated files, so the directory date means nothing. Always use the line's `timestamp`.
- **Grok CLI** (`~/.grok/sessions/<cwd>/<sessionUuid>/`, every directory including subagent forks):
  - Preferred source: `updates.jsonl` lines with `params.update.sessionUpdate == "turn_completed"`. `params.update.usage` holds per-turn, non-cumulative `inputTokens` (includes cached reads), `outputTokens` (includes reasoning), `cachedReadTokens`, `cacheCreationTokens`, `reasoningTokens`, `modelCalls`, and `modelUsage{model:{same fields}}`.
  - Emit one event per model in `modelUsage` (or one event with the session model if `modelUsage` is absent). nativeKey is `params._meta.eventId + ":" + model`.
  - Mapping: `cacheR=cachedReadTokens`, `cacheW=cacheCreationTokens`, `in=max(0,inputTokens-cachedReadTokens-cacheCreationTokens)`, `out=outputTokens`, `reasoning=reasoningTokens`, `calls=modelCalls`.
  - ts: the line's top-level `timestamp` (epoch seconds).
  - Fallback 1, a session with no `turn_completed` lines but a `usage.json`: use `turns[]`, nativeKey `sessionId:turnNumber:endedAt`.
  - Fallback 2, neither available: emit nothing. Never sum `_meta.totalTokens`; it is the context-window fill.

- **Cursor** (v1.2; the IDE's global state database, read-only):
  - File: Windows `%APPDATA%/Cursor/User/globalStorage/state.vscdb`; macOS `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb`. SQLite. Open read-only (`mode=ro`) and never take a write lock; Cursor may have it open. Read via a pure-Go driver (`modernc.org/sqlite`), the only non-stdlib dependency allowed for this.
  - Table `cursorDiskKV`: `composerData:<composerId>` rows are sessions (JSON: `createdAt`, `lastUpdatedAt`, `modelConfig.modelName`, `unifiedMode`, and a bubble list); `bubbleId:<composerId>:<bubbleId>` rows are messages (JSON: `type` 1 = user, 2 = assistant; `createdAt` when present; `tokenCount: {inputTokens, outputTokens}`; `usageUuid`; `modelInfo.modelName`; text fields).
  - Usage event per assistant bubble with a non-empty `tokenCount`: `in=inputTokens` (Cursor does not split cached input, so cacheR=0 and cacheW=0), `out=outputTokens`, `reasoning=0`, `calls=1`, `q=exact`.
  - nativeKey: `usageUuid`, falling back to `composerId:bubbleId`.
  - ts: the bubble's `createdAt`; if absent, the composer's `createdAt` (about a third of bubbles lack their own timestamp). Timestamps may be epoch ms or ISO strings.
  - model: bubble `modelInfo.modelName`, else composer `modelConfig.modelName`, else "".
  - session: `composerId`. workspace: the composer's folder if recorded, else "".
  - Prompts: user bubbles (`type` 1) with text; nativeKey `composerId:bubbleId`; model from the next assistant bubble in the same composer, else the composer model.
  - Account: the builder locates Cursor's cached account identity in `ItemTable` (keys under `cursorAuth/`) and hashes it like the others; if none is found, events are `acctQ=unknown`.
  - Cursor rewrites the database in place, so this source is a **whole-file** source: the cursor stores the file's size and mtime and the parser re-reads all rows when either changes, relying on the id rules for idempotency. Parsing must not read the 2 GB file into memory: iterate rows.
  - The site's mock data, provider filter, hues and pricing family fallbacks gain Cursor. Cursor's model ids (e.g. `claude-4.5-sonnet-thinking`, `gpt-5.1-codex-max-xhigh`, `gemini-3-pro`, `grok-code-fast-1`) are priced by family fallback to the underlying model where a verified list price exists; otherwise they are unpriced.

- **Gemini CLI** (v1.3; provider `google`, source `gemini-cli`):
  - Files: `~/.gemini/tmp/<projectHash>/chats/session-*.json`. Each is a whole JSON session that Gemini rewrites as it grows, so it is a **whole-file** source (re-read on size/mtime change; Offset=Size).
  - Shape: `{sessionId, projectHash, startTime, lastUpdated, messages[{id, timestamp, type: user|gemini|info, content, thoughts, tokens{input, output, cached, thoughts, tool, total}, model, toolCalls}]}`.
  - Usage event per `gemini` message with `tokens`: Gemini's `input` includes cached content, so `in = max(0, input − cached) + tool`, `cacheR = cached`, `cacheW = 0`, `out = output + thoughts`, `reasoning = thoughts`, `calls = 1`, `q = exact`. The builder verifies against `total` on real data and adjusts the mapping if the identity does not hold.
  - nativeKey `sessionId:messageId`; ts = message `timestamp`; model = message `model`; session = sessionId; workspace: the project folder if the session or a sibling file records it, else the projectHash.
  - Prompts: `user` messages; nativeKey `sessionId:messageId`; model from the next `gemini` message.
  - Account: `~/.gemini/google_accounts.json` (an account id or email; hash it, never store it). `oauth_creds.json` is never read.

## Scan roots: multiple homes (v1.3)

- The collector scans a list of **homes**, each with every parser and its own account probes: the OS user home, any `extraHomes` from `config.json`, and, on Windows, discovered WSL homes.
- **WSL discovery** (Windows only, `discoverWsl: true` by default): each tick runs `wsl.exe -l --running -q` (UTF-16LE output) and, for each running distro, scans `\wsl$<distro>home*` and `\wsl$<distro>
oot` that contain any of `.claude`, `.codex`, `.grok`, `.gemini`. **A distro that is not running is skipped and never started** (touching `\wsl$<distro>` would boot it). `status` lists each distro as scanned or "not running, skipped". Usage inside a stopped distro cannot change, so nothing is missed, only delayed.
- Cursors are keyed by absolute path, so UNC paths need no special handling. Account timelines are keyed by (provider, home). The machine label is the same for all homes; a heartbeat lists the homes scanned.
- Config fixes (retention) are applied per home as well, with the same backup rules.

## IDs

`id = hex(sha256("v1|" + kind + "|" + provider + "|" + source + "|" + nativeKey))[:32]`, where `kind` is `usage`, `activity` or `prompt`. There is no machine, path, offset or secret in the id. Helpers live in `internal/model` and are mirrored in `api/src/lib/ids.js` for tests.

## Accounts

- The collector reads the active account each tick:
  - Claude: `~/.claude.json` `oauthAccount.accountUuid`. The label candidates are `emailAddress` and `organizationName`.
  - Codex: `auth.json` `tokens.id_token` JWT claims `https://api.openai.com/auth.chatgpt_account_id`, falling back to `tokens.account_id`. The label is the `email` claim.
  - Grok: every entry of `~/.grok/auth.json`, `user_id`. The label is `email`. If there are several entries, the one with the latest token expiry is active.
- `acct = "a_" + hex(HMAC-SHA256(K, provider + "|" + nativeAccountId))[:16]`. K is the 32-byte fleet key shared by all machines through the join code, so the same account hashes identically everywhere.
- Timeline: `state.json.accounts[]` holds `{provider, home, acct, from, to}` intervals, extended while unchanged (the live probe).
- **Attribution qualities (v1.7).** Every usage, activity and prompt record carries `acctQ`; the server keeps the higher rank:

  | acctQ | rank | meaning | prompt label |
  |---|---|---|---|
  | `recorded` | 6 | The tool's own log names the identity for this event's stream (file, rollout, process), written before the event with no login boundary in between: Claude `credential_org`, Codex `creator_account_id`, Grok `user_info` joined by pid to the session, a Cursor log folder's owner. | yes |
  | `session` | 5 | A Claude SessionStart hook spool entry for that sessionId. | yes |
  | `timeline` | 4 | ts inside a live probe interval (within one tick). | yes |
  | `bounded` | 3 | Between two login boundaries whose identity samples all agree (or between two agreeing samples), or a file provably written once (Gemini `google_accounts.json` with an empty `old`). An org-only record whose org is not in the org map also lands here. | yes |
  | `lineage` | 2 | A continuous chain into a known account, not proven per event: a Codex plan era running into a recorded anchor, a Grok token chain with no interactive login in between, the only account a tool (Cursor) ever named. | no |
  | `inferred` | 1 | The nearest (or sole) candidate in time, nothing more. | no |
  | `unknown` | 0 | No candidate. | no |

- **Attribution order** (`accounts.Resolve` over `evidence.Index`), first hit wins:
  1. The stream's own record (the parser's hint, a session record from the evidence, the Grok pid join, a Cursor log window), if no login boundary lies between it and ts: `recorded`. An org hint maps to its account through the local org→account map; an unmapped org is hashed as native id `org:<uuid>` and drops to `bounded`. Two recorded sources that disagree: the later record wins and a conflict is counted.
  2. The live timeline, only if it says `timeline`.
  3. Claude Code (only when login boundaries are known) and Gemini: the login interval around ts. All samples agree: `bounded`. Disagreeing samples: `bounded` between two agreeing samples, else the nearest sample as `inferred` plus a conflict.
  4. Lineage, per provider: Codex plan eras (`plan_type` carried per rollout; a change inside one live rollout, an upgrade flap or a dip, does not break the era; a change between rollouts does; an era takes the account of the recorded anchors inside it when they agree), the Grok token chain, Cursor between two samples of one account or a sole account.
  5. The nearest sample (live probe intervals included): `inferred`.
  6. `unknown`.
  Attribution is by timestamp, never by sessionId alone: a session that changes credentials mid-way is split at the `credential_org` record.
- **Evidence** (`collector/internal/evidence`), harvested each tick before the scan, per home, resuming from watermarks (`state.json.harvest`), and kept in `state.json.evidence` so it outlives the tools' own pruning and rotation:
  - Claude: `credential_org` attachments per transcript (also carried in the parser's cursor, so an incremental read keeps it); `bridge-session` owners and `artifact-autoreact-ledger` accounts (they have no timestamp: the last timestamped line before them gives it); `~/.claude/backups/*` and `~/.claude.json.backup` `oauthAccount` (a sample at the file's mtime); the desktop app's `claude-code-sessions/<account>/<org>/` folders (createdAt..lastActivityAt); `history.jsonl` `/login` and `/logout` lines as boundaries; the org→account map from all of these and `~/.claude.json`.
  - Codex: `session_meta.creator_account_id` per rollout and `state_5.sqlite threads.creator_account_id` per thread (session records); `logs_2.sqlite` "Reloading auth for account" rows newer than the stored row id (samples, and session records when the row names its thread); `token_count.rate_limits.plan_type` runs per rollout. The databases are opened `mode=ro&immutable=1`.
  - Grok: `logs/unified.jsonl`: `auth init user_info check` `ctx.user_id` per pid, which sid ran in which pid, and `auth started` with a method other than `cached_token` (an interactive login) as the token chain's boundary.
  - Gemini: `google_accounts.json` `active` at the file's mtime, and whether `old` is empty. `oauth_creds.json` is never opened.
  - Cursor: `logs/<YYYYMMDDTHHMMSS>/…/Cursor Indexing & Retrieval.log` `owner: <provider>|user_…` lines; each folder is a window from its name (local time) to its last write.
  - The live probe's intervals are samples too, so a probe observation and a harvested record agree on one hash.
- **Re-attribution:** `evidence.AttribVersion` is stored in `state.json.attribVersion`. When the harvest has completed and the stored version is lower, the tick resets every source cursor once, so history is re-read and re-sent; the server merge upgrades each event in place (higher `acctQ` wins) and the prompt upsert upgrades prompts (see Server prompt upsert). A tick whose harvest ran out of budget defers its scan, so history is never attributed from half the evidence.
- **Privacy:** harvesters decode only identity fields into narrow structs, never a token field, never `oauth_creds.json`, Codex or Grok `auth.json` (those stay with the existing probe), or `.credentials.json`. Every native id is hashed at once (`acct` formula above; an org as `org:<uuid>`); evidence records hold `{provider, home key, kind, source enum, acct/org hash, session key, pid or log-folder name, plan name, ts, to, q}` and nothing else: no raw id, email, prompt text or path. Emails and org names found in snapshots become default local labels in `config.json` only, as the probe's do. `scan --dry-run --json` reports counts only (`byAcctQ`, `conflicts`).
- Labels never go into usage events. Labels exist in local `config.json` (`accountLabels: {acct: label}`; default `email · org`) and go only inside prompt records, which the server encrypts.
- **Prompt labels follow the quality (v1.7; v1.6 allowed only timeline/session).** A prompt record carries `acctQ`, and `acctLabel` only when `acctQ` ranks 3 or higher (`recorded`, `session`, `timeline`, `bounded`); below that it is `""` (the `acct` hash is still sent). The archive shows the quality next to the label ("Anthropic · label (recorded)") and groups lower ranks as "Unattributed (probably <label>)" when the candidate account has a label elsewhere, else "Unknown account". A rank-3+ attribution to an account with no local label (for example an org seen only in `credential_org`, or a Cursor id seen only in its logs) is its own group, "Unnamed account (<quality>)".

## Wire format: collector → server

All requests go to `https://d0m1.com/api/...` with JSON bodies and a max body of 1 MB. Auth uses header **`X-D0M1-Token: <machine token>`**. Never use `Authorization`, because SWA overwrites it.

### Automatic account history (collector v0.2.4)

The collector reads Codex account history automatically through the installed, already signed-in Codex client's `app-server` protocol: `initialize`, `initialized`, then `account/usage/read`. This is a read-only request; it does not create a thread or run a model. The child is hidden on Windows and time-limited. A first successful run backfills every returned daily bucket; subsequent reads run every 15 minutes. Changed totals upload immediately; unchanged snapshots refresh hourly so corrections observed by another machine cannot leave stale server totals. Missing clients, signed-out accounts and unsupported older protocol versions do not stop ordinary local collection. Credentials stay with Codex, and diagnostic messages contain no provider response bodies or secrets.

These records are account-wide snapshots, not extra machine usage. They use the same fleet account hash as local Codex events. The API keeps the newest snapshot per account/source/date and subtracts matching local token records across every machine before adding any recovery. Repeated uploads, including uploads from another machine, do not add the same snapshot again; later-arriving local records reduce the recovered remainder. Source-reported dates are reconciled against UTC event dates, a normalization convention rather than a documented guarantee about Codex's backend boundary.

Recovered counts enter the public response as `unattributed` tokens and are displayed as **Account history**. They contribute to Total, but not to Input, Output, Effective, price, request counts, or a fabricated model/project/machine. Machine-specific views retain only records attributable to that machine. Account/workspace hashes remain private. Missing prompt-only records are still marked without estimating tokens.

Weak historical account assignments are reserved as possible overlap rather than counted as a proven separate account. If closed-day totals conflict across the returned historical window, recovery for that window is withheld instead of inventing a daily allocation; the footer reports the conflict. The current day's potentially delayed provider reading is treated separately so it cannot remove historical recovery. Incomplete reconciliation likewise appears in the footer until its coverage is ready.

### POST /api/ingest

```jsonc
{
  "v": 1,
  "collectorVersion": "0.1.0",
  "sentAt": "2026-09-29T10:00:00Z",
  "usage": [UsageEvent],        // ≤ 200
  "activity": [ActivityEvent],  // ≤ 500
  "prompts": [PromptRecord],    // ≤ 50 and ≤ 512 KB total
  "limits": [LimitSnapshot],    // ≤ 50, optional. Newest observedAt per id wins.
  "accountUsage": [AccountUsageSnapshot], // ≤ 90, optional; newest account/day snapshot wins
  "heartbeat": Heartbeat        // optional
}
```

`AccountUsageSnapshot` carries `{id, provider: "openai", source: "codex", acct, acctQ: "recorded", date: "YYYY-MM-DD", timezone: "UTC", totalTokens, observedAt}`. The account table's private `usage` partition stores snapshots independently of the reporting machine. Rollups carry a private UTC account ledger for reconciliation; that ledger is never returned publicly. An account-history upload upgrades only the surrounding local-day rollups needed for comparison, including empty dates. A retry does not rescan already upgraded history.

`LimitSnapshot` (v1.8) is the plan window a tool has already written, read again each scan. It is not a usage event. Every 5 minutes (per provider, recorded in `state.json` `quotaRefreshed`, only for enabled sources whose client is in use in the OS home), the collector asks each installed, signed-in client for a fresh reading just before the scan, without reading credentials, calling a provider API itself or sending a prompt: Claude Code via `claude -p --input-format stream-json --output-format stream-json --verbose --no-session-persistence --setting-sources local --strict-mcp-config` with control requests `initialize` then `get_usage` only (no user settings, so no hooks; Claude Code then rewrites its own `cachedUsageUtilization`); Codex via `codex app-server` `initialize` then `account/rateLimits/read`, whose answer is written in Codex's rollout format to `<collector home>/quota/codex-rate-limits.jsonl` and read like a rollout (same meter id; the newest reading wins); Grok via `grok agent stdio` (ACP) `initialize` then `_x.ai/billing`, which records the billing config in Grok's own log. Clients run hidden in `<collector home>/quota/work`, time-limited, with stderr and RPC error bodies discarded; each is given end of input and up to 3 s to exit so it can save its meter. A missing client or failure never stops collection. Claude Code reads `~/.claude.json` `cachedUsageUtilization` (session, week, a scoped week such as Fable, and extra usage). Codex reads the newest `token_count.rate_limits` in each rollout (primary and secondary windows). Grok reads `subscription_tier_display` from `settings_cache.json` and weekly `billing: fetched credits config` records from `.grok/logs/unified.jsonl`. Its `creditUsagePercent` and `currentPeriod.end` are provider-reported usage and reset time; the unified allowance is shared across Grok products. A billing record requires a preceding identity check for the same process, with sign-in/process boundaries respected. An omitted percentage means zero only for a recognized unified weekly configuration, matching Grok Build's own handling. Grok writes these snapshots during usage, `/usage`, its own polls and the collector's periodic `_x.ai/billing` request. Cursor and Gemini CLI write no meter. The id is `EventID("limit", provider, source, acct|window|scope)`. The collector queues a snapshot when its content changes, or when the provider re-reported an unchanged meter (its `observedAt` advanced by at least 5 minutes since the last queued copy), so the page's reading age tracks the provider's report rather than the last change in value. Synthetic `plan` rows stamped with local time are re-queued only on change. The server stores the row on the accounts table, partition `lim`, and replaces it only when `observedAt` is newer. Percent is not fieldwise-maxed, because a window resets and the percent falls. `name`, `plan`, `scope` and `detail` are short labels and are rejected if they contain `@`. `label` is the account email, so accounts can be told apart, and it is returned only by the owner-only `GET /api/limits`. No raw account id is stored. `GET /api/usage` does not return these rows.

The `/tokens/agents` page shows **weekly quota available** (`100 − usedPercent`): a full green bar means 100% available and an empty red gauge means exhausted. Session counters are omitted; a session restriction is still indicated. Reset countdowns are greener as the weekly reset approaches, with the exact local date/time and the source reading's age shown separately. Expired readings are not treated as a fresh allowance. Each account has a checkbox; unticked accounts live in a collapsed **Other** section. Choices persist per browser origin under `d0m1.agents.tracking.v1`, keyed by provider and normalized email (native account fallback), separately from mock data. Initial defaults track accounts with valid weekly meters, then remain stable through expiry and refresh. An explicitly tracked account without a meter remains visible with a missing-reading message. Plans always have a label; unavailable information is stated explicitly. Optional private browser-local plan corrections (`d0m1.agents.plan-labels.v1`) are account-scoped and apply only while the reported source label matches, so later provider plan changes take precedence. These preferences do not change collection or upload behavior.

```jsonc
{ "id": "…32 hex…", "provider": "anthropic", "source": "claude-code",
  "acct": "a_…", "acctQ": "recorded",
  "plan": "Max (20x)", "window": "week", "scope": "Fable",
  "name": "Example Org", "label": "person@example.com",
  "usedPercent": 60, "resetsAt": "2026-10-06T15:00:00Z",
  "observedAt": "2026-10-01T02:35:48Z", "status": "ok",
  "detail": "" }
```

`window` is `session`, `week`, `extra` or `plan` (or another short token the tool used). `status` is `ok`, `full` or `disabled`. Claude's `is_active` only marks the limit its UI leads with (an inactive 3% session beside an active 60% week), so it is not a status. Rows stored by older collectors may say `paused`; readers ignore it. `usedPercent` and `resetsAt` are omitted when the tool did not write them.

### GET /api/limits (owner only)

→ `{ "items": [LimitSnapshot, …] }`. Anonymous callers get 401, a signed-in non-owner gets 403. `/tokens/agents` is owner-only, noindex and never prerendered. It groups accounts by provider and email (`src/components/agents/limits.ts`), shows weekly quota available and reset countdowns, and refreshes every minute. An expired snapshot is marked as awaiting a reading; it never implies a replenished allowance. Internal tier codes (`default_…`) are not shown as plan names. See the weekly quota presentation and tracking preferences above.

`UsageEvent`:

```jsonc
{ "id": "…32 hex…", "provider": "anthropic", "source": "claude-code",
  "ts": "2026-09-28T23:59:58.120Z",   // UTC; the MIN source timestamp seen for this key
  "tzOffsetMin": 60,                  // machine offset at ts (minutes east of UTC)
  "model": "claude-opus-5-5",
  "acct": "a_1f2e…", "acctQ": "timeline",
  "q": "exact",                       // "exact" | "estimated"
  "pv": 1,                            // parser version for this source
  "session": "s_…16 hex…",            // hex(sha256(provider|sessionId))[:16]
  "ws": "w_…16 hex…",                 // optional (v0.2.0): HMAC-SHA256(K, "workspace|" + repo root)[:16]
  "in": 0, "cacheW": 0, "cacheR": 0, "out": 0, "reasoning": 0, "calls": 1 }
```

`ActivityEvent` (one per user prompt; it drives the "inferred" tier and the prompt counts):

```jsonc
{ "id": "…", "provider": "anthropic", "source": "claude-code", "ts": "…", "tzOffsetMin": 60,
  "acct": "a_…", "acctQ": "inferred", "session": "s_…", "ws": "w_…", "hasUsage": false }
```

`ws` (collector v0.2.0+, both kinds) tags the event with its workspace, the same git-root folder a prompt for that cwd stores, keyed with the fleet key so the server and the public never see the path. It is stored on the event row and merged like `session` (smaller non-empty wins); no read API returns it. It is there so a later estimate can rate tokens per prompt per (provider, workspace), shrunk toward the provider rate; the v1 estimate still uses the provider rate.

`hasUsage` is true when the prompt came from a transcript that also yields exact usage. It is false for prompt-history-only data such as Claude `history.jsonl` before the transcripts.

`PromptRecord`:

```jsonc
{ "id": "…", "provider": "anthropic", "source": "claude-code", "ts": "…", "tzOffsetMin": 60,
  "model": "claude-opus-5-5",        // "" if unknown
  "acct": "a_…", "acctQ": "recorded", // v1.7; absent from older collectors (see Server prompt upsert)
  "acctLabel": "person@example.com · Org",   // "" unless acctQ ranks 3+: recorded/session/timeline/bounded (see Accounts)
  "workspace": "C:\\Users\\User\\src\\d0m1.com", "machine": "desktop-win",   // the git repository root of the cwd (see Workspaces)
  "session": "s_…", "text": "…" }    // text capped at 256 KB (truncated with a marker)
```

Prompt sources:

- **Claude:**
  - Transcript lines with `type=user`, not `isMeta`, not `isSidechain`, not `isCompactSummary`, whose content is a string or text blocks and not a `tool_result`. Also skipped: `[Request interrupted by user…` markers, lines whose `origin.kind` is set and not `human` (task notifications), and text starting with `<` unless `promptSource` is `typed`.
    - nativeKey: `uuid`.
    - model: the next assistant message's model in the same file, or "".
  - Transcript lines with `type=attachment`, `attachment.type=queued_command`, `commandMode=prompt`, `origin.kind=human`, not `isMeta`, not `isSidechain`: a prompt typed while a turn was running, which Claude Code delivers inside that turn and never writes as a user line. nativeKey: the line's `uuid`; the text is `attachment.prompt`.
  - Plus `~/.claude/history.jsonl` entries (`display`, `timestamp`, `project`, `sessionId`), **only when no transcript `<sessionId>.jsonl` exists anywhere under projects**.
    - nativeKey: `hist:sessionId:timestamp`.
    - These produce ActivityEvents with `hasUsage=false`.
- **Codex:**
  - `response_item` with `payload.type=message` and `role=user`, whose joined text does not start with `<` or `# AGENTS`.
    - The `<image name=[Image #N]>` / `</image>` text blocks that frame an attached image are not text: they are dropped before the check, and a message with only framed images is `[image]`. A message with an `input_image` and no text block at all is a tool result (`view_image`), not a prompt.
    - Skipped: rollouts whose first `session_meta` is a subagent thread (`thread_source=subagent` or `source.subagent`), and lines with `metadata.inherited_user_message` (fork copies).
    - nativeKey: `rolloutId:ordinal`, where ordinal is the top-level `ordinal` if present, else the 0-based line index.
  - `history.jsonl` only for session_ids with no rollout file (nativeKey `hist:session_id:ts`).
- **Grok:** `updates.jsonl` `user_message_chunk` updates. A message is one text block plus the image blocks after it, so a run of consecutive chunks is split at each further text block (messages typed mid-turn are written back to back and share the running turn's `promptId`).
  - nativeKey: `params._meta.eventId` of the message's first chunk.
  - Skipped: messages with a `hideFromScrollback` chunk, text starting with `<system-reminder>`, and sessions whose `summary.json` has `session_kind` or `parent_session_id` (subagents).

`Heartbeat`:

```jsonc
{ "machineLabel": "desktop-win", "os": "windows", "arch": "amd64", "version": "0.1.0",
  "scanAt": "…", "clockSkewMs": 0, "outboxEvents": 0,
  "sources": { "claude-code": {"files": 850, "lastEventTs": "…"}, "codex": {…}, "grok-cli": {…} },
  "checks": { "claudeRetention": "ok|fixed|failed|skipped", "grokRetention": "…", "codexHistory": "ok|warn", "smartAppControl": "off|on|unknown", "autostart": "ok|missing" },
  "tz": { "iana": "Europe/Berlin", "windowsId": "W. Europe Standard Time", "country": "DE", "source": "windows+region" },   // v1.1, from internal/tzinfo.Detect()
  "homes": ["C:\\Users\\me", "\\\\wsl$\\Ubuntu-22.04\\home\\me"] }   // v1.3, optional: the scan roots (≤ 16 strings of ≤ 260 chars); stored on the machines row, never returned by /api/usage
```

Response `200`:

```jsonc
{ "ok": true, "accepted": ["id", …], "retry": ["id", …], "serverTime": "…",
  "rejected": [{ "id": "…", "error": "…" }],        // invalid items (id null for a bad heartbeat); never retried
  "warnings": [{ "id": "…", "warning": "…" }] }     // v1.1: accepted with a fix-up, e.g. "cacheW1h clamped to cacheW"
```

Ids in `retry` hit the server's time budget, and the client re-sends them. Anything else in the request is accepted, including duplicates, which are harmless. Error responses:

- `401`: bad token. The client stops uploading, keeps collecting, and `status` shows it.
- `413`: halve the batch.
- `5xx` / timeout (client timeout 60 s, because managed Functions cold-start in 10–30 s): retry with backoff, halving on consecutive failures (floor 10).

### POST /api/enroll

Request:

```jsonc
{ "invite": "…", "machineLabel": "…", "os": "…", "arch": "…", "version": "…", "kFingerprint": "…16 hex = sha256(K)[:16]…" }
```

Response:

```jsonc
{ "machineId": "m_…", "token": "…43-char base64url…" }
```

- The invite is single-use and expires after 15 minutes.
- The server stores only `sha256(token)`.
- The first enrollment pins `kFingerprint` in the `accounts` table (PK `fleet`, RK `k`). A mismatch returns 409.

### POST /api/invite (X-D0M1-Token of an enrolled machine, or an owner principal; see Owner auth)

→ `{ "invite": "…", "expiresAt": "…" }`.

The collector's `invite` command prints the join code `D0M1-<invite>-<base32(K) no padding>`. K never reaches the server.

**Bootstrap (first machine):**

- `node api/scripts/create-invite.js` writes an invite row directly, using `AGENTS_STORAGE_CONNECTION_STRING` from the env or from `az storage account show-connection-string -n <account> -g <group> -o tsv`, and prints it.
- The first machine installs with `--join D0M1-<invite>`, with no K part. The collector then generates K.
- If the server already has a fleet fingerprint, enrolling without K (or with a different K) returns 409, with the message "join code must come from `tokenmaxr invite` on an enrolled machine".

### Linking a machine (v0.2.0): no join code to type

There is one fleet and one owner, so installs hide the join code. The mechanics above stay underneath (`install --join CODE` and `invite` still work).

- **Fleet key escrow.** `accounts` PK `fleet`, RK `kenc` holds K encrypted with `PROMPT_ENC_KEY` (AES-256-GCM, AAD `fleet-k`). The server makes K on the first `/api/link` when no fingerprint is pinned yet. A fleet whose K was made by a machine (fingerprint `k` pinned, no `kenc`) cannot be linked: `/api/link` answers 409 `NOT_ESCROWED`, and that fleet keeps using `invite` codes. Escrow means the server can read K; that is accepted because the owner is the only user and the prompts it protects are already encrypted with a server key.
- **POST /api/link** (owner only; 503 without `PROMPT_ENC_KEY`): makes a normal 15-minute invite (createdBy `link:<idp>:<userId>`) and returns `{ "join": "D0M1-<invite>-<base32 K>", "expiresAt": "…" }`.
- **Collector.** `install` (and the desktop app started when not enrolled) with no `--join` listens on `127.0.0.1:0`, makes a random state (24 bytes, base64url) and opens `<endpoint>/collector/link?port=P&state=S`. It waits up to 15 minutes.
- **`/collector/link`** checks port (1024–65535) and state, POSTs `/api/link`, and on success replaces itself with `http://127.0.0.1:P/callback?state=S&join=…`. On 401 it offers GitHub / Microsoft sign-in that returns to the same URL; on 403 it says the account is not the owner. The code only ever goes to this machine's loopback.
- **Callback.** The listener accepts only the matching state and a parseable join code, redirects to `/collector?linked=1` with `Referrer-Policy: no-referrer`, then enrolls with that code exactly as `--join` would. The stable page says sign-in is complete while setup finishes. The loopback server shuts down gracefully, allowing the callback response to finish before its connection closes (v0.2.1).

### Owner auth (private endpoints)

SWA injects `x-ms-client-principal` (base64 JSON `{identityProvider, userId, userDetails, userRoles}`) into managed Functions. A caller is the owner if `userRoles` contains `owner`, or if `identityProvider + ":" + userDetails`, lowercased, is in the comma-separated app setting `AGENTS_OWNER_IDS` (set to `github:7-of-9,aad:dom@d0m1.com`). Anything else gets `401` (not logged in) or `403` (logged in, not owner). The page logs in through `/.auth/login/github` or `/.auth/login/aad` with `post_login_redirect_uri=/tokens/prompts`.

## Server storage (an Azure storage account; app setting `AGENTS_STORAGE_CONNECTION_STRING`; optional `AGENTS_TABLE_PREFIX` for tests)

| table | PK | RK | content |
|---|---|---|---|
| `eventindex` | first 2 hex of id | id | `date` (YYYY-MM-DD). **Pinned on first sight**, so an id never moves between days. |
| `events` | date | id | `kind` (usage/activity), provider, source, ts (min), model, acct, acctQ, q, pv, session, token fields (incl. cacheW1h), calls, hasUsage, **`machine`** (machineId of the first reporter) and **`cc`** (that machine's country at first insert). `machine`/`cc` are set on insert and never changed by merges. |
| `dirtydays` | `d` | date | `gen` (int), `updatedAt` |
| `rollups` | `r` | date | `gen`, `data` (JSON string, see below), `computedAt` |
| `machines` | `m` | machineId | tokenHash, label (**public** since v1.1; updated from each heartbeat's machineLabel), os, arch, version, enrolledAt, lastSeenAt, heartbeat (JSON string), `tzIana`, `cc` (from heartbeat `tz`), `homes` (JSON string array from heartbeat `homes`, v1.3; private) |
| `invites` | `i` | invite | expiresAt, usedAt, createdBy |
| `accounts` | `a` / `fleet` | acct / `k` | provider, firstSeen, lastSeen (no labels); fleet K fingerprint |
| `prompts` | month `YYYY-MM` (UTC) | id | ts, provider, source, model, acctQ (plaintext; acctQ since v1.7, see Server prompt upsert); `enc` = base64(AES-256-GCM(iv‖ciphertext‖tag)) of `{text, workspace, acctLabel, acct, machine, session}`. If the ciphertext exceeds 30,000 base64 chars it goes in blob `prompts/<id>` and `blob=true`. |

- **Day bucketing:** `date = (ts + tzOffsetMin)` as a local calendar date. The first ingest of an id pins it in `eventindex`.
- **Merge rule** (events are upserted with an ETag-checked read-modify-write):
  - Same `pv`: fieldwise **max** of `in, cacheW, cacheW1h, cacheR, out, reasoning, calls`; `ts = min`.
  - Higher `pv`: replace.
  - Lower `pv`: ignore.
  - `acct`/`acctQ`: keep the higher quality (`recorded 6 > session 5 > timeline 4 > bounded 3 > lineage 2 > inferred 1 > unknown 0`; equal ranks: the smaller hash). Validation accepts exactly these seven.
  - `model`: keep non-empty.
- **Rollup:** before writing a day's events, increment `dirtydays.gen`. After writing, recompute that day's rollup from the whole `events` partition, but only if that day's rollup is older than 20 s (v1.1 backfill speed-up). Otherwise leave it dirty. Write it only if the stored rollup gen is not newer (ETag-guarded). `GET /api/usage` lazily recomputes any day that is still dirty. A dirty day is recomputed at most 60 s after its last write, so the public page is never more than about a minute behind.
- **Prompt encryption:** AES-256-GCM with app setting `PROMPT_ENC_KEY` (base64 32 bytes), 12-byte random IV, AAD = id. Prompt upserts are idempotent by id.
- **Time budget:** stop processing at 30 s. Return the unprocessed ids in `retry`.

Rollup `data` (v3; `"v": 3`; v1/v2 rollups are recomputed on read):

```jsonc
{ "v": 3, "date": "2026-09-28",
  "providers": {
    "anthropic": {
      "exact":     { "in":0,"cacheW":0,"cacheW1h":0,"cacheR":0,"out":0,"reasoning":0,"calls":0,"events":0 },
      "estimated": { … same shape … },
      "prompts": 12,            // all activity events
      "promptsNoUsage": 0,      // activity with hasUsage=false: prompt-only days (tokens never recorded)
      "accts": ["acct1"],       // per-response aliases in /api/usage (never hashes)
      "models": { "exact": { "claude-opus-5-5": { "in":0,"cacheW":0,"cacheW1h":0,"cacheR":0,"out":0,"reasoning":0,"calls":0 } },
                  "estimated": { … } },          // empty model keyed "unknown"
      "byMachine": { "m_…": { "in":0,"cacheW":0,"cacheR":0,"out":0,"prompts":0 } },   // exact+estimated tokens, all activity; no reporter -> "unknown"
      "byCountry": { "DE":  { "in":0,"cacheW":0,"cacheR":0,"out":0,"prompts":0 } }    // from events.cc ("" -> "ZZ" unknown)
    } } }
```

- Rows written before v1.1 have `machine=""`; the first sighting after v1.1 fills `machine` and `cc` once, and they never change after that. Rows never re-sent stay under `byMachine["unknown"]` / `byCountry["ZZ"]`, so both still add up to the provider totals.
- **Workspace splits (v3):** `byWorkspace` groups the same exact/estimated buckets, prompts, promptsNoUsage, models, byMachine and byCountry by the event’s opaque `ws` tag. Place splits within a workspace also carry `promptsNoUsage`. Untagged events belong to `unknown`. Public responses replace tags with per-response `workspace1`, `workspace2`, … aliases shared across providers and days; no paths, names or persistent workspace hashes are exposed. Old events need collector replay to gain tags; recomputation alone cannot recover a missing tag.
- Until a v1 rollup has been recomputed (at most 120 days per `/api/usage` request), it is served in this v2 shape with `"v": 1` and empty `models`, `byMachine` and `byCountry`, so no day disappears. A pending v2 rollup retains its existing models and place splits until its v3 recompute.

## Read API

### GET /api/usage?days=371|all (public, `Cache-Control: public, max-age=60`)

`days=all` returns every day since the first data. The page's Overview uses it for the year selector.

```jsonc
{ "generatedAt": "…", "lastIngestAt": "…", "firstDate": "2026-01-01",
  "days": [ RollupData v3, … ],               // only days with data
  "machines": [ { "id": "m_…", "label": "Studio Mac", "cc": "DE", "os": "darwin", "live": true, "lastSeenAt": "…", "firstSeenAt": "…" } ],  // v1.1, public by owner decision
  "totals": { "accounts": 5, "accountsByProvider": {"anthropic":2,"openai":2,"xai":1}, "machines": 2, "machinesLive": 1 } }
```

Since v1.1 this endpoint exposes machine labels and countries, both public by owner decision. It still returns no account ids or labels, workspaces or project names, or prompt text. Account hashes appear only as per-response aliases and counts. v1.1 drops `ratios`.

### GET /api/prompt-counts (public, `Cache-Control: public, max-age=60`)

Returns archive counts only: `{source: "archive", generatedAt, total, undecryptable, days: [{date, providers: {[provider]: {prompts, byModel: {[model]: count}, byMachine: {[publicMachineId|"unknown"]: {prompts, byModel}}, byMachineComplete}}}]}`. Dates use each prompt's recorded timezone offset. Empty models belong to `unknown`; model and machine counts each sum to the provider's archived prompt count.

Machine attribution accepts an existing public machine ID or an exact, case-sensitive, unique public machine label. Missing, unmatched and ambiguous labels belong to `unknown`; `byMachineComplete` is false whenever that provider/day contains an unknown machine. Raw machine names are never returned. `undecryptable` reports records excluded because their private archive payload could not be read.

These counts use the existing server-side archive index and share its 60-second cache, concurrent build and ingest invalidation. They may differ from `/api/usage` activity counts: prompt capture and activity reporting are separate records. They do not infer prompts from usage calls or token counts. Public output contains no prompt contents, record IDs, account identifiers or labels, sessions, workspace paths or names. `/api/prompts` and its item/facet routes remain owner-only.

### GET /api/prompts[?month=all|YYYY-MM&order=asc|desc&workspace=&provider=&model=&acct=&machine=&q=&acctLabel=&limit=&cursor=&facets=1] (owner only; see Owner auth)

→ `{ "month": "all"|"YYYY-MM", "order": "asc"|"desc", "items": [{ id, ts, tzOffsetMin, month, provider, source, model, acct, acctQ, acctLabel, acctProbable, workspace, workspaceKey, workspaceLabel, machine, session, text, textPreview, truncated }], "months": ["2026-09", …], "total": 1234, "cursor": "…"|null, "undecryptable": 0, "facets"?: … }`, in `order` **across months**.

- `order` (v1.7): `asc` (the default) lists oldest first, `desc` newest first; both sort by `ts`, then `id`, so ties are total and one order is the exact reverse of the other. Anything else is 400. The cursor records its order: a cursor from one order is 400 in the other (a cursor from before v1.7, without an order, continues `desc`). Paging covers every match exactly once in either order, over all months or one month, with any filters.
- `month` missing or `all` (v1.6 default) means the whole archive; `YYYY-MM` is one UTC month partition.
- Filters: `provider`, `model`, `machine` and `acct` match exactly (`acct` is an account facet key, see below); `workspace` is a workspace facet key (or a raw workspace exactly as stored); `q` (prompt text) and `acctLabel` are case-insensitive substrings. `total` counts every match; `limit` (default 500, max 2000) and the opaque `cursor` page through them.
- Account fields (v1.7, see Accounts): `acctQ` is the stored attribution quality (the row's plaintext `acctQ`; for a row written before v1.7, `timeline` if its payload has a label, `inferred` if it has an acct, else `unknown`). `acctLabel` is shown only when `acctQ` ranks 3 or higher (recorded, session, timeline, bounded), else `""`. `acctProbable` is, for an item below that threshold whose candidate account has a label on any labelled prompt, that label (newest first); else `""`. `acct` is the item's account facet key. `GET /api/prompts/{id}` also returns `acctQ`.
- `facets=1` adds the facets below to the response.
- Items carry at most a 4 KB preview; `GET /api/prompts/{id}?month=<item.month>` returns the full text.
- **Scale:** every list and facet request is served from one per-process index of the whole archive: all month partitions listed (8 at a time, only the needed columns), metadata and inline text decrypted, sorted newest first, workspaces canonicalised and facets counted. It lives for 60 s; an ingest that writes prompts drops it on that instance. 20,000 prompts build in about a second on the memory store (`handlers.test.js`); blob prompts are indexed by their encrypted preview, so blob storage is never read for a list.

### GET /api/prompts/facets (owner only)

The filter values across the **whole archive**, each with its prompt count, from the same index:

```jsonc
{ "generatedAt": "…", "total": 1234, "undecryptable": 0,
  "providers":  [{ "provider": "anthropic", "count": 900 }],                        // SPEC provider order
  "accounts":   [{ "acct": "a_…~recorded", "provider": "anthropic", "label": "…", "quality": "recorded", "count": 120, "unattributed": false },
                 { "acct": "unknown:anthropic:a_…", "provider": "anthropic", "label": "", "quality": "", "count": 60, "unattributed": true, "probable": "…" },
                 { "acct": "unknown:anthropic", "provider": "anthropic", "label": "", "quality": "", "count": 720, "unattributed": true }],
  "workspaces": [{ "key": "c:\\users\\me\\src\\app", "label": "app", "path": "C:\\Users\\me\\src\\app", "paths": ["C:\\Users\\me\\src\\app", "C:\\Users\\me\\SRC\\app\\ui", …], "count": 145 }],
  "models":     [{ "value": "claude-opus-5-5", "count": 700 }],
  "machines":   [{ "value": "desktop-win", "count": 1234 }],
  "months":     [{ "month": "2026-09", "count": 300 }] }                            // newest first
```

- Account facet keys (v1.7). A prompt with `acctQ` ranked 3 or higher and a label or an acct hash is keyed `<acct hash>~<acctQ>` (`label:<provider>:<label>~<acctQ>` if it has no hash), so one account appears once per quality; `label` is the newest label seen (`""` for an unnamed account) and `quality` the `acctQ`. Anything below the threshold (lineage, inferred, unknown, or neither label nor hash) has `unattributed: true` and `quality: ""`: keyed `unknown:<provider>:<acct hash>` with `probable` = that account's newest label when its candidate account has a label on some labelled prompt, else pooled under `unknown:<provider>`. Order: SPEC provider order, labelled before unattributed, `probable` groups before the plain unknown one, then count (desc), label, key.
- Workspaces are grouped by their canonical root (see Workspaces); `label` is the root's basename, `path` the root as most often written, `paths` the raw workspaces folded in (most frequent first, at most 50). Prompts with no workspace are `key: "none"`, label "(no workspace)".
- `/api/prompts/facets` is served by the `prompts/{id}` function (the id `facets` is reserved; ids are 32 hex).

## Workspaces (v1.6: one rule set, `api/src/lib/workspaces.js`, mirrored in `collector/internal/sources/workspace`)

The same folder reaches the archive in several spellings, so the API canonicalises every raw workspace at read time. Both implementations pass the shared vectors in `collector/internal/sources/workspace/testdata/vectors.json`.

1. **Normalise.** Windows paths (drive or UNC) are case-folded with `\` separators and no trailing separator (`C:\Users\me\SRC\app\` → `c:\users\me\src\app`). `/mnt/<drive>/…` becomes the Windows path; `\\wsl$\<distro>\…`, `\\wsl.localhost\<distro>\…`, Cursor's `vscode-remote://wsl+<distro>/…` (also `wsl%2B`) and a Linux path written with backslashes (`\home\me\…`, a single leading `\`) become the Linux path (the distro is dropped). POSIX paths keep their case (repeated and trailing `/` removed). `file://` URIs become paths. A tool's per-project data folder, `…/.cursor/projects/<slug>` or `…/.claude/projects/<slug>` (Cursor records these as a composer's folder), is replaced by its slug. Other remote URIs (`vscode-remote://ssh-remote+…`) are kept as written.
2. **Slugs.** A name with no separators and a `-` (Claude project-directory names such as `C--Users-me-src-app` or `c-Users-me-src-app`, WSL ones such as `home-me-src-app`) is matched by its slug form (lowercase, each run of non-alphanumerics one `-`, `mnt-<drive>-` read as `<drive>-`) against every known real path **and its ancestors**; an ambiguous form goes to the path with more prompts. Unmatched slugs are decoded best effort: `<d>-users-<u>-src-<rest>` → `<d>:\users\<u>\src\<rest>`, `home-<u>-src-<rest>` → `/home/<u>/src/<rest>`, `users-<u>-…` → `/Users/<u>/…` (the rest keeps its dashes). A 64-hex Gemini project hash is matched against sha256 of each known real path, else kept (label `gemini <first 8>`).
3. **Fold.** A workspace moves to the **nearest ancestor that is itself a workspace in the set and is not a generic container**, repeated until none applies. Generic containers are never fold targets: a drive or share root, `C:\Users`, a user home (`C:\Users\<u>`, `/home/<u>`, `/Users/<u>`, `/root`), `/mnt` and `/mnt/<drive>`, and any folder named `src`, `source(s)`, `repo(s)`, `code`, `projects`, `dev`, `git`, `github`, `work`, `workspace(s)`, `documents`, `desktop`, `downloads`, `tmp`, `temp` or `sandbox`. So `…\src\app\collector` folds into `…\src\app` when `…\src\app` has prompts of its own, `…\src\tmp\x` stays separate, and a generic container that has prompts (for example the home folder) stays its own workspace.

**Collector side (new records).** The prompt's workspace is the **git repository root** of the cwd: the nearest ancestor (the cwd included) that contains `.git` (a directory, or a file for worktrees and submodules), else the cwd as the tool wrote it. For a cwd below the scanned home, the search stops before the home itself, so a dotfiles repository in the home never swallows the projects under it. Linux paths from a WSL home are probed through that distro's `\\wsl$` share and returned in Linux form. Lookups are cached per process for 10 minutes. Ids never depend on the workspace, so parsers stay deterministic.

## Prompts page (`/tokens/prompts`, v1.6)

- Filters, in order: **Workspace, Month, Provider, Model, Account, Machine**, then the search box. Month defaults to **All months**, so choosing only a workspace shows all of its prompts.
- Every dropdown lists every value in the whole archive (from `/api/prompts/facets`) with its count, e.g. `mum-dna (145)`, `Sep 2026 (300)`, `All months (1,234)`.
- Workspace options show the flattened basename (disambiguated by the parent folder when two roots share one), with the full canonical path as the tooltip.
- Account options are grouped by provider and read `Anthropic · <email · org> (recorded) (n)`, one per account and quality (`recorded`, `session`, `timeline`, `bounded`; an account with no label reads `Anthropic · Unnamed account (bounded)`); below them `Anthropic · Unattributed (probably <email · org>) (n)` for prompts whose inferred or lineage candidate is a known account, and `Anthropic · Unknown account (n)` for the rest. Items show the same wording without the provider (it is already on the line). A collapsed "How accounts are attributed" legend under the filters explains each quality in one line and says that labels appear only for recorded, session, timeline and bounded.
- A prominent total for the current filter ("1,234 prompts") sits above the list, with the filter scope in words, "showing the oldest n" (or "newest n") while more pages remain, and "Clear filters".
- Sort order (v1.7): a small segmented toggle "Oldest first | Newest first" right of the total. The default is oldest first. The choice is kept in `localStorage` key `d0m1.tokens.prompts.order` (reads and writes wrapped in try/catch; blocked storage falls back to oldest first), so it survives workspace, month and filter changes, "Clear filters" and refreshes. Changing it reloads from the first page. Pages of 100 load in that order; "Show 100 newer prompts" (oldest first) or "Show 100 older prompts" (newest first) continues with the cursor. The mock archive (`?mock=1`) honours the order too.
- Same frame and panels as the rest of `/tokens`; the total and the toolbar are static (no hover), only controls respond. Owner auth is unchanged.

## Heatmap normalisation (read time only; never stored)

- **Metrics:**
  - **tokens** (raw, the default everywhere, by owner decision): `in + cacheW + cacheR + out`, meaning all input including cached plus all output. "In" in the headline is `in + cacheW + cacheR`; "out" is `out`.
  - Detail also offers **effective** (`in + cacheW + out + 0.1 × cacheR`) and **output** (`out`).
- **Tiers:**
  - **exact**: the `exact` bucket.
  - **estimated**: the `estimated` bucket (future web-chat estimates).
  - **Prompt-based inference is disabled** (owner decision, 2026-10-02). The page never calls `inferDays`; the checkbox is removed and old `?estimate=1` links are ignored. `infer.ts` and its tests remain as historical implementation, outside the page's dependency graph. Recorded token buckets supplied by the API remain unchanged.
- **Activity-only days** (prompts recorded, tokens never recorded: `promptsNoUsage > 0`) add **no tokens** to any headline, graph, feed, table or cost. A day with no recorded tokens is drawn empty with a green outline (see Visual design). A day that also has recorded tokens shows its recorded level; provider/project tooltips show how many prompts have no token records. No tokens-per-prompt multiplication or daily smoothing runs.
- Cell colour: recorded tokens (exact + estimated, or exact only when the toggle is on) mapped to **GitHub's four levels by quartile** of the non-empty days in the period (see Visual design). There is no hatching. Cells that are mostly estimated keep their level, and the tooltip says so.
- Days follow each event's local date (see day bucketing).

## Visual design (/tokens): owner direction v1.5 (2026-09-30), GitHub's profile, overrides the earlier thermal-ramp design

The owner wants `/tokens` to look and read like a GitHub profile's contribution graph and "Contribution activity" feed: copy the layout, the style and the colours closely. It must not be a wall of text or look like a generic AI-generated dashboard. The thermal ramp, the canvas bloom, the continuous colour scale and the gradient legend are gone.

- **Frame.**
  - `/tokens`, `/tokens/prompts` and `/collector` keep the `/projects` header: the fixed, full-width `< d0m1 / tokens` bar (GalleryPage's own classes, via `TokensShell`), the background video exactly as on every other page (no dimming or blur), and the site footer. The home menu lists `tokens // agent usage` between projects and resume.
  - **Max width.** The content column is centred with `max-width: 1280px` (gutters included; `agents-base.css .agents-page`). On `/tokens` that leaves GitHub's ~1012 px graph column plus the year list beside it. The header stays full width. `/collector` keeps its narrower 960 px column, also centred.
  - **One static green sheet.** `/tokens` sits on one panel with the /projects tile look (near-black green pane, grey-and-green double border, deep shadows) that stands in for GitHub's page background. Inside it everything is GitHub dark: the system sans at 14 px, `#f0f6fc` ink, `#9198a1` muted text, `#3d444d` borders, 6 px corners.
  - **No hover on anything that is not a control.** Panels, the graph box, Detail boxes, the collector and prompt panels (including the public/private facts panel) have no hover glow, lift or border change. Hover states are only on real controls: links, buttons, segmented controls, the year list, fold toggles, "Show more", and heatmap days (which open a tooltip).
  - `?mock=1` shows only the yellow full-width "Test data" banner (on `/tokens` and `/tokens/prompts`). There is no extra "test data" pill beside the view switch or anywhere else.
- **Graph (GitHub's contribution calendar).**
  - Headline above the box, in GitHub's form with the full comma-grouped number: **"45,348,234,278 tokens in the last year"** (or "… in 2025"), with the Overview / Detail segmented control where GitHub puts "Contribution settings". It is 16 px regular in the GitHub system font and default ink (no green, no glow), with only the figure ("17.6B") in semibold, as the figures in the tables are. Directly under it, a second line gives the period's **monthly** rate, set like the cost tile's "$977 / month": the figure in the shared `.agents-figure` style (24 px semibold, default ink) and the unit beside it in the shared muted `.agents-figure__unit` style, e.g. **"1.47B tokens / month"** ("effective tokens / month", "output tokens / month" for the other metrics). Monthly = the period total (in the chosen metric and provider filter) ÷ (days in the period / 30.44); a current calendar year counts only the days so far, and the last 12 months count as 12, the same rule as the monthly cost. Checked at 1920, 1280 and 390 px.
  - The box has a 1 px `#3d444d` border and 6 px radius. Inside:
    - Weeks run as columns (Monday first), always 53 of them. A partial current year is padded to 31 December with dimmed blank cells, so switching period never changes the graph's size.
    - The cells size to fill the column at GitHub's 10:3 cell-to-gap ratio (about 14 px cells in the 1280 px layout), with a radius of about 20 % of the cell (GitHub's 2 px at 10 px). On a phone they drop to GitHub's own 10 px and the graph scrolls sideways. It opens on the newest week, and the weekday labels stay pinned.
    - Month labels and Mon / Wed / Fri labels are 12 px in the default ink, as on GitHub.
  - **Colours:** GitHub's dark palette as github.com draws it today, sampled from the owner's reference screenshot. Empty `#151b23`, then levels `#033a16`, `#196c2e`, `#2ea043`, `#56d364`. Every cell has GitHub's faint 1 px inner outline (`rgba(255,255,255,0.05)`). The older Primer set (`#161b22`, `#0e4429`, `#006d32`, `#26a641`, `#39d353`) is superseded; `heat.ts GH_LEVELS` and `AgentsPage.css` hold the values.
  - **Levels:** GitHub's rule. The non-empty days in the shown period are split at their quartiles: level 1 up to Q1, level 2 up to the median, level 3 up to Q3, and level 4 above. So each green holds about a quarter of the active days, and one extreme day is simply level 4. Days with no recorded tokens are level 0.
  - **Activity-only days** (prompts but no recorded tokens): the empty colour with a faint green outline. There is no count to colour, so they are never drawn as a green level. The tooltip says so, and a small "tokens not recorded" key appears in the legend only when such days are shown.
  - **Estimated-dominant days** keep their level; the tooltip adds "mostly estimated".
  - **Footer row** in 12 px muted text:
    - Left, where GitHub has "Learn how we count contributions": `in 45.1B · out 295M · ~4.1B / month · 7 accounts · 5 providers · 2 live`. The monthly average is over months with data in the period.
    - Right: **"Less ▪▪▪▪▪ More"** with the five palette swatches.
  - **Tooltip:** Primer's grey `#3d444d` bubble with white 12 px text. It leads with GitHub's sentence, **"218,289,498 tokens on September 26th."** or "No tokens on September 29th." A quieter line gives in / out, the prompt count and the day's API-equivalent cost, e.g. "in 2.2M · out 53.8K · 39 prompts · ≈ $4.12", priced in the mode the Detail cost tile's "Prices" toggle has saved (at the time by default; the same localStorage setting, so Overview and Detail agree), with "+ unpriced" when some of the day's tokens are on models with no list price. Then come providers (when there is more than one) and the top three models. The year is added to the date only outside the current year.
- **Period list:** beside the content column (sticky under the header), like GitHub's. "Last 30 days", "Last 60 days", "Last 90 days", then "Last 12 months" (the default) and each year back to `firstDate`. The selected period persists in `?period=30d`, `60d`, `90d` or the year; absent/invalid values use Last 12 months. All periods filter the graph, headline, feed and Detail together. Short windows include today plus the preceding 29/59/89 days and render numbered, Monday-first month calendars with the same heat colours and day tooltips. Dates outside the selected window are dimmed and inert. The API supplies daily aggregates, so no hourly values are invented. The chosen entry is filled in GitHub's button green `#238636` with white text. On tablets and phones the list becomes a row above the headline.
- **Token activity feed (Overview only), copying GitHub's "Contribution activity":**
  - A "Token activity" heading. Then, newest month first, a divider (**September** 2026 with a rule to the edge) and Primer Timeline items: a 2 px rail, a 32 px round badge with a line icon, and a 16 px title. Each item has GitHub's fold toggle on the right; it collapses the item's rows. Items per month, in order:
    1. **"Used 21,982,770,636 tokens across 12 models"**, then one row per model: `claude-opus-5-5  11,915,567,315 tokens`, largest first. Each row has a right-aligned bar whose length is the row's share of the month. The largest row is GitHub's emphasis green `#2ea043` and the rest are its light green `#9be9a8`, as on GitHub. More than 8 models ends in "Show N more models". This replaces GitHub's repositories.
    2. **"Ran on 2 machines"**: flag, public machine label, tokens (or prompts on activity-only months), and bars the same way.
    3. **"Started using claude-fable-5-1"** / "Started using 4 models": models whose first recorded tokens fall in this month. Each row shows the provider with a dot where GitHub shows a repository's language, and the first date flush right. This is the counterpart of GitHub's "Created 1 repository". The month history starts in gets no "started" item, because there is no history to compare against. Placeholder ids such as `unknown` never count as started.
    4. **"2,835 prompts with tokens not recorded"** when the month has activity-only prompts.
    5. **"First activity recorded"** with its date, on the month history starts (GitHub's "Joined GitHub").
  - The feed opens on the two newest months. GitHub's full-width outline **"Show more activity"** button loads two more months per click.
  - **Project and workspace names never appear.** The feed is built from rollup model buckets and machine ids only (`activity.ts`). Model names and machine labels and countries are public.
- **Detail** (the segmented switch; the feed gives way to it):
  - The filters (provider, metric, exact only) are small Primer segmented controls between the headline and the graph they filter. Provider keys are GitHub-style colour dots. The metric control reads **"Total | Effective | Output"** (the internal keys stay `tokens`, `effective`, `output`; the headline nouns stay "tokens", "effective tokens", "output tokens"). Each button's title says what the count is: "in + out (incl. cached)", "in + out (cached at 10%)", "out only".
  - Then GitHub boxes on the sheet (1 px border, 6 px corners, 16 px semibold titles, no hover), in this order:
    1. **API-equivalent cost**, directly after the graph: the big 24 px semibold figure is the **monthly** cost, "$977 / month", with the period total beside it in the muted caption style, "$11,718 over the last year" (or "in 2026"). Monthly = the period's cost ÷ its length in months, where months = days in the period / 30.44; a current calendar year counts only the days so far, and the last 12 months count as 12. Both follow the Prices toggle. Then the by-model table (model, in, out, cached share, cost), sorted by cost. Each row has a GitHub activity bar ranking the models (the top row in emphasis green, the rest light green). Pricing works as in "Detail" below.
       - **Prices: at the time | today**, a small segmented control on the tile's header (only the buttons have hover), defaults to **at the time** and is saved in localStorage (`d0m1.tokens.prices`, read and written in try/catch). At the time prices each day's per-model buckets at the list price in effect on that local date (`history` windows in `modelPricing.json`, from the priced model the lookup resolves to) and sums the days; today prices everything at today's list price. The dollar figure, the per-model costs, the sort and the bars follow the toggle. Where the other mode gives a different figure, the cost cell's (and the total's) hover title shows it, e.g. "$412 at today's prices".
       - The footnote reads "Standard API list prices at the time of use; long-context surcharges not applied." or "Standard API pricing at today's list prices; long-context surcharges not applied."
    2. **Over time** (Day / Week / Month). A single series (one provider, or only one with data) is drawn in GitHub green `#2ea043`. A multi-provider stack keeps distinct but muted provider hues (`--ag-anthropic` …, about a third of the way to grey), with a dot legend and each provider's share.
    3. **By workstation** and **By region**, side by side: flag, label, live dot, tokens, share and a GitHub bar.
- **Flags** are SVG (country-flag-icons), never emoji, because Windows cannot render flag emoji. Each flag is its own lazy chunk, used by the feed and Detail.
- `/collector` follows the same restraint: a one-line intro, the two copyable commands, and at most three short lines on privacy and uninstall. It keeps the site's faces and the static panel look.

## Collector behaviour

- **State dir:** `%LOCALAPPDATA%\tokenmaxr` (Windows) or `~/Library/Application Support/tokenmaxr` (macOS). `TOKENMAXR_HOME` overrides it (tests).
  - `config.json`: endpoint, machineLabel, accountLabels, enabled sources, flags.
  - `secrets.json` (0600 / user-only ACL): token, K (base64), machineId.
  - `state.json`: cursors, account timeline, account evidence and harvest watermarks (`evidence`, `harvest`, `attribVersion`; see Accounts), backoff, lastUpdateCheck. Written atomically (tmp + rename).
  - `outbox/`: `NNNNNNNNNN.json` batch files.
  - `collector.log`: rotated at 5 MB.
  - `collector.lock`, `bin/`.
- **Tick** (`run`), started every minute by the OS:
  1. Take the lock (non-blocking; exit 0 if it is held).
  2. Run config checks and fixes (once per hour).
  3. Probe accounts.
  4. Scan sources.
  5. Write new events to the outbox **then** save cursors.
  6. Upload the outbox.
  7. Send a heartbeat (every 5 minutes, or on the first tick).
  8. Self-update check (hourly, and once when the desktop app starts; only the signed manifest is fetched unless a newer version is out).
  9. Exit.

  Hard time limit: 50 s, apart from the first-run backfill, which continues across ticks.
- **Cursors:** keyed by absolute path, storing `{size, mtimeNs, offset, headHash (sha256 of first 4 KB), pv, carry}`.
  - `carry` holds parser context: Codex rolloutId/model/lastTotal/epoch/sawRecord, Claude message max-usage cache for in-flight ids, the Grok prompt-chunk buffer.
  - v1.1: Claude keeps its in-flight usage keys only while the file's last usage line is under 30 minutes old (`sources.CarryTTL`); the scan's idle window is 35 minutes so the tick that drops them always runs.
  - Every tick stats every file.
  - A file that shrank, has a changed headHash, or a pv bump is reparsed from 0.
  - A partial last line (no `\n`) is never consumed.
  - Once a week there is a full reparse at low priority.
  - Whole-file JSON (Grok `usage.json`) is reparsed when its mtime or size changes.
  - Windows opens files with FILE_SHARE_READ|WRITE|DELETE.
- **Config checks:**
  - Claude `~/.claude/settings.json` `cleanupPeriodDays` < 3650: set it to 3650 with a minimal text edit that preserves other content. Back it up once to `settings.json.d0m1-backup`. If the file can't be parsed, refuse to edit.
  - Grok `~/.grok/config.toml` `[storage] cleanup_ttl_days` set and non-zero: set it to 0.
  - Codex `config.toml` `[history] persistence = "none"`: warn.
  - Windows Smart App Control state (`HKLM\SYSTEM\CurrentControlSet\Control\CI\Policy\VerifiedAndReputablePolicyState`): report.
  - `--no-fix-config` disables all edits.
- **Autostart:**
  - macOS: LaunchAgent `com.d0m1.collector` with `StartInterval=60`, `RunAtLoad`, `ProcessType=Background`, `LowPriorityIO`, `Nice=10`.
  - Windows: a per-user Task Scheduler task `\d0m1\collector` built from XML:
    - triggers: LogonTrigger with UserId set to the current user, plus a TimeTrigger repeating every PT1M indefinitely;
    - settings: `StartWhenAvailable`, both battery flags off, `MultipleInstancesPolicy IgnoreNew`, `ExecutionTimeLimit PT5M`;
    - runs `tokenmaxrw.exe run`.
- **Commands:**
  - Global flag: `--home DIR` overrides the state dir; so does env `TOKENMAXR_HOME`.
  - `install --join CODE [--endpoint URL] [--label NAME] [--yes] [--no-fix-config] [--no-autostart] [--no-prompts]`
    - Idempotent: re-running it repairs or upgrades.
    - On a first install without `--label` it asks for the public machine label (default: the hostname; macOS reads /dev/tty, Windows the console only when stdin is one). With no terminal it uses the hostname and prints a one-line public notice. A label already in config.json is kept on re-install; `--label` replaces it. Labels are cleaned like the API's (control and bidi characters dropped, whitespace collapsed, at most 48 characters).
    - It copies the running binary (and, on Windows, the sibling `tokenmaxrw.exe`) into `<home>/bin/`.
    - It adds `<home>/bin` to the user PATH: HKCU `Environment\Path` on Windows; on macOS a `~/.local/bin` symlink plus a printed hint.
  - `run`: one tick (what the OS scheduler starts).
  - `label NEW_NAME` (v1.1): renames the machine (words are joined, quotes optional), saves config.json and sends a heartbeat at once; if offline, the next tick sends it.
  - `sync-now [--since DATE]`: a verbose foreground loop of ticks until the outbox is empty. `--since` restricts events for tests.
  - `scan --dry-run [--json] [--since DATE] [--until TS]`:
    - parses all sources from offset 0, uploads nothing, touches no state;
    - prints totals per source × provider × month: usage events, the token fields, effective, activity (hasUsage true/false), prompts, files, first/last ts;
    - also groups those totals by workspace (`byWorkspace`). The folder is the one prompts already store (the git root of the cwd). Usage and activity take that same cwd. The groups are then folded with the prompts-page canonicalisation. An empty key is events whose log recorded no folder. The field is not uploaded; public usage rows still carry no project;
    - attributes like a tick, from evidence harvested afresh into memory plus the live probe (the enrolled key, else a throwaway one), and adds per source `byAcctQ: {usage|activity|prompts: {acctQ: count}}` and `conflicts` (counts only);
    - prints `limits`, the newest plan window each tool has written (see LimitSnapshot). The text has the account email, the plan, the window, the percent and the reset. It does not print account hashes;
    - `--json` emits the same as JSON, used by the oracle cross-check.
  - `status` also prints an `evidence` line (record counts per provider and kind, watched files, attribution version). `doctor`, `invite`, `uninstall [--purge]`, `version`.
  - v1 has **no** Claude SessionStart hook; attribution follows Accounts (recorded, timeline, evidence). The hook is a future option.
- **Prompt capture controls** in `config.json`:
  - `prompts: true|false` (default true).
  - `promptExcludeAccts: ["a_…"]`: accounts whose prompts are never captured. Their usage and activity still flow.
  - Core applies both after parsing.
- **Parser packages** `internal/sources/{claude,codex,grok,cursor,gemini}` each export `func New() sources.Source`. Files are opened with `internal/fsx.Open`, which sets Windows share-delete.
- **Claude prompt model:** a prompt's model is the next assistant line's model. When a prompt is the last complete line so far, the parser keeps the full PromptRecord in `Cursor.Carry` (pending) and emits it once the next assistant line arrives, or after 10 minutes with model "".
- **Server prompt upsert:** idempotent by id. A new row stores `acctQ` in plaintext beside `enc`. A prompt with no `acctQ` (a collector before v1.7) is taken as `timeline` if it has a label, `inferred` if it has an acct, else `unknown`; a label is dropped when `acctQ` ranks below 3. If the row exists:
  - **Upgrade (v1.7):** when the incoming `acctQ` ranks above the stored one (the row's `acctQ`, or for an older row the legacy rule over its decrypted payload; an undecryptable row is never rewritten), the payload is decrypted, only `acct` and `acctLabel` are replaced (text, workspace, machine and session stay as first written), and it is re-encrypted: `enc`, or for a blob row the blob and `penc`. The new `acctQ` is stored, an empty `model` is filled in the same write, and the write is ETag-checked (re-read and retried on 412).
  - **Otherwise** `enc` is never rewritten: if the stored `model` is empty while the incoming one is non-empty, update only `model`.
- **Self-update:**
  - Fetch `https://d0m1.com/collector/latest.json` and `latest.json.sig`, then verify the ed25519 signature with the public key embedded in the binary.
  - Manifest: `{version, minVersion, files: {"windows-amd64": {url, sha256, size}, "windows-amd64-w": {…}, "darwin-arm64": {…}, …}}`.
  - Download, check the sha256, swap binaries at the end of a tick (on Windows, rename the running exe to `.old` first), and clean up `.old` on the next tick.
- **Upload:** send `usage`, `activity` and `prompts` in batches under the caps. Delete each outbox file once all of its ids are accepted. Rewrite it with only the `retry` ids when needed.

## Release and publishing (single pipeline, hardened after the ops review)

- `collector/VERSION` (and optional `collector/MIN_VERSION`) must be `MAJOR.MINOR.PATCH`. Prerelease suffixes are rejected.
- **Every change to the collector's shipped sources requires a VERSION bump.** A release is pinned to the collector/ sources that first published its VERSION. The manifest's top-level `source` field is a sha256 over `git ls-tree -r HEAD` of `collector/`, excluding `cmd/signmanifest/`, `scripts/`, `testdata/`, `*_test.go` and `*.md`. Changing sources without a bump fails CI with "bump collector/VERSION". Clients ignore `source`.
- **macOS build (v1.4).** The macOS `tokenmaxr` is also the menu-bar app (Cocoa), so it needs cgo and cannot be cross-built on Linux:
  - A small `collector_release_check` job compares `collector/VERSION` with the live `d0m1.com/collector/latest.json` (never a CDN release URL). Only when they differ (or the live one cannot be read) does `collector_macos_job` run on `macos-14`.
  - That job builds `./cmd/tokenmaxr` with `CGO_ENABLED=1`, `MACOSX_DEPLOYMENT_TARGET=12.0` and the release flags below, for arm64 and for amd64 (`CC="clang -arch x86_64"`), and uploads the workflow artifact `collector-darwin`: `VERSION` plus `darwin-arm64/tokenmaxr` and `darwin-amd64/tokenmaxr`. It receives no secrets.
  - The build job `needs` it but runs under `!cancelled()`, so a failed or skipped macOS build never blocks a site deploy. It downloads the artifact and passes it as `--prebuilt DIR`.
- The SWA workflow runs these steps on push to `main`, after the site build:
  1. **Publish** (`node scripts/publish-collector.js --prebuilt DIR`, storage key only). If `collector/v<ver>/latest.json` exists, it is reused **only after all of these pass**:
     - its stored signature (blob metadata `signature`) verifies against the release key;
     - every file URL equals exactly `https://cdn.d0m1.com/d0m1-media/collector/v<ver>/<key>/<file>`;
     - the `source` fingerprint matches;
     - every blob's sha256 and size match the signed manifest.

     Anything else fails the run: the pipeline **never re-signs bytes it did not build**. Otherwise it produces the six artifacts:
     - cross-built here with `CGO_ENABLED=0`: `windows-amd64`, `windows-amd64-w`, `windows-arm64`, `windows-arm64-w`;
     - taken from `--prebuilt DIR`: `darwin-arm64`, `darwin-amd64` (file `tokenmaxr`, cgo). `DIR/VERSION` must equal the release version, and each file must be a thin 64-bit Mach-O of its key's CPU. They are checked before any Go build; if they are missing or wrong, the run fails before anything is uploaded;
     - flags: `-trimpath -buildvcs=false -ldflags "-s -w -X main.version=<ver> -X main.buildTime=<time>"`, plus `-H windowsgui` for `-w`. `<time>` is the RFC 3339 UTC commit time of the latest `collector/VERSION` change (CI: `COLLECTOR_BUILD_TIME`, resolved from the GitHub API), never the wall clock, so every platform and any rebuild of a release carry identical bytes. The tray popup shows it as a recessive footer, e.g. `v0.2.6 · built 2026-10-04 08:15 UTC`.

     It uploads them **write-once** (`ifNoneMatch: *`, immutable caching; an existing blob must hash identically) and writes an unsigned `dist/collector/latest.json`. The manifest key order is the list above: `windows-amd64`, `windows-amd64-w`, `windows-arm64`, `windows-arm64-w`, `darwin-arm64`, `darwin-amd64`. There are no other keys. A missing blob in a published release is restored only from identical bytes. For a macOS blob, that means a `--prebuilt` build of that version, which the check job does not trigger once the version is live, so a deleted macOS blob fails loudly until VERSION is bumped. A cgo build is reproducible on the same runner image and Go version. If a rerun after a partial upload gets different macOS bytes, it fails with "bump collector/VERSION".
  2. **Sign** (`go run ./cmd/signmanifest -expect-pubkey <release key>`). **Only this step receives `COLLECTOR_SIGNING_KEY`.**
  3. **Finalize** (`publish-collector.js --finalize`). It verifies the `.sig`, then uploads `latest.json` write-once with the signature in its metadata, atomically. If a concurrent run won, it adopts that run's verified manifest.
  4. **Restore**, when this run did not publish, including PR previews: fetch the live `d0m1.com/collector/latest.json` and `.sig` into `dist/collector/`. A live 404 means no release yet, and the deploy continues with a warning.
  5. **Check** (`publish-collector.js --verify dist/collector`). If dist does not hold a correctly signed manifest with all six keys, the job stops **before Deploy**, so the previous deployment stays live.
  6. After Deploy, the run is marked **failed** (red) if the release did not publish.
- `minVersion` is **advisory** until the collector enforces it. The planned enforcement: force-apply the update and pause uploads below minVersion.
- Self-update must only download from the `https://cdn.d0m1.com/d0m1-media/collector/v<ver>/` prefix. This is defence in depth on top of the signature.
- The release public key (base64, raw 32 bytes) embedded in the collector is `LSnlPhe0k5DKraLM8K7cCUrwdEWNhpL/jQgzQicgQIA=`. A binary built with version `dev` never self-updates.
- `latest.json` and `.sig` exist only in `dist/collector/`; they are never committed under `public/`. The install and uninstall scripts in `public/collector/` are copied to `dist/` by `scripts/vite-plugin-selective-public-copy.js` and served as `text/plain` with no-cache.
- **Install scripts** download only the keys above (`<os>-<cpu>`, plus `-w` on Windows), check SHA-256 and run `tokenmaxr install`. They pass every other flag through, including `--no-app` (headless mode; see Desktop app). The uninstall fallbacks, used when the binary is missing or its `uninstall` fails, stop the app and remove the LaunchAgent `com.d0m1.collector` (macOS), or the scheduled task, the HKCU Run value `tokenmaxr` and the running `tokenmaxrw.exe` (Windows).
- `scripts/publish-collector.test.js` (`node --test scripts/publish-collector.test.js`) exercises the dry run and the publish, sign, finalize and reuse flow against an in-memory blob store with a test signing key, stub macOS binaries and a throwaway Go module.
- **Cloudflare:** `cdn.d0m1.com` caches 404s (30 days observed). Never request a release URL before it is uploaded. If one was requested, purge those exact URLs through the Cloudflare API (`CLOUDFLARE_API_TOKEN` in `.env`). A cache rule that does not cache 4xx on `cdn.d0m1.com/d0m1-media/collector/*` is recommended (see `CLOUDFLARE_ERROR_CACHE_FIX.md`).

## Local testing

- `api/test/harness.js` serves the pure API handlers at `http://127.0.0.1:7071/api/*`, with `AGENTS_TABLE_PREFIX=<prefix>` so tables are isolated. Tests create and delete their prefixed tables.
  - The harness accepts `x-ms-client-principal` from the request, for testing owner routes. Production relies on SWA to inject it.
- The Vite dev server reads `AGENTS_API_PROXY` from the shell or `.env.local`: `http://127.0.0.1:7071` selects the local harness, and `https://d0m1.com` selects the production API and its Azure datastore. Restart Vite after changing the setting. The `/tokens` footer shows the actual upstream API origin (local, production or remote); `?mock=1` says no API. Production builds use their own origin and ignore the dev proxy.
- `AGENTS_DEV_OWNER=1` supplies a fake owner only with a loopback harness; it is never sent to production. The public production usage API needs no sign-in. Private production routes still require the real owner session on d0m1.com.
- For local private pages against production, set `AGENTS_AUTH_BRIDGE` in `.env.local` to the absolute path of the persistent-CDP skill's `scripts/client.mjs`, alongside `AGENTS_API_PROXY=https://d0m1.com`. Start/reuse that skill's bridge with `scripts/ensure.ps1`, then restart Vite. This opt-in development integration is absent from production builds.
- Local sign-in opens the real Azure/GitHub flow in Chrome and returns to the original local path, query and fragment. One HttpOnly, SameSite=Strict grant covers the private pages on that exact loopback origin. The grant lives in the persistent bridge, survives Vite restarts, and has a seven-day idle expiry renewed by successful production checks; production authentication/owner access is revalidated on every request. A lost helper tab is recreated only for an existing explicit grant. Bridge/network failures are retryable errors, not sign-outs; a real production 401 revokes the local grant. Local sign-out revokes this origin's grant without signing out unrelated production tabs. Chrome cookies remain in Chrome.
- The local bridge allows only `GET /.auth/me`, `GET /api/limits`, prompt list/facets/detail reads, and the existing `POST /api/link` action with an exact same-origin request and no body. Public API requests retain the normal Vite proxy. The shared header reads the same auth state as the private pages, so a valid header login and the archive cannot silently use different sessions. Production continues to use its native same-origin Azure sign-in/out endpoints.
- Run `node --test scripts/vite-plugin-dev-agents-auth.test.js` for the local bridge's session, origin, method, owner-denial and failure-path checks.
- Token pages retain their original fonts, with the site's fixed navigation typography and no font-preview controls. Period totals form the primary headline; the per-active-day figures are slightly smaller beneath it. Initial loads and refreshes share Projects' circular `LoadingIndicator`; token downloads retain byte, phase and elapsed-time detail.
- `/tokens` polls the selected API every 15 seconds while visible, bypasses the browser cache, and refreshes immediately on return to the tab. A per-API-origin session snapshot (maximum 24 hours old) makes reloads display the last good public aggregates immediately while updating; the displayed timestamp stays honest. Requests time out after 12 seconds and retry after 1, 2, 4… seconds up to 15 seconds. A transient refresh failure is a small footer status; three consecutive failures show the full notice. Initial loads show an animated progress bar, elapsed time, and downloaded bytes; a percentage is shown only when an uncompressed response supplies a usable Content-Length. Server rollups can still take up to their dirty-day grace period to settle during a backfill.
- The top machine selector defaults to all machines and persists as `?machine=<id>`. It uses the production workspace/reporter splits to retain each machine's models and quality buckets. A shared workspace without those per-machine details keeps its reported token total under an unknown model, displays a notice, and disables exact-only filtering. Fleet account counts are omitted from a single-machine view because the API does not attribute them per workspace.
- All day tooltips, in year heatmaps and short month calendars, share one layout: a slightly larger date/total heading, recorded tokens, provider totals, then anonymous project rows. Projects with missing token records show the prompt count without an estimate. Models are always expanded, with counts and the same proportional green bars as the Overview feed (largest model in emphasis green, others light green). Numbers use K/M/B/T and dates use month names. Project numbers are the public API's anonymous workspace aliases. Clicking a day (or Enter/Space when focused) pins the tooltip for scrolling; content stays within the viewport. Arrow keys navigate between days.
- `api/test/load.js` (`npm run load`) measures ingest throughput with synthetic events on random-prefix tables, dropped afterwards.
- `/tokens?mock=1` renders synthetic data with no API.

## Security

- The machine token can only ingest, heartbeat and invite. It cannot read prompts.
- The public read endpoint returns aggregates only.
- The prompts endpoint is protected by the in-function owner check (Owner auth). A SWA route rule adds `allowedRoles: ["authenticated"]` on `/api/prompts` as defence in depth, with no global 401 redirect; the page shows its own login buttons.
- Prompt text is encrypted at rest in the app with a key held in SWA app settings. The Azure storage account is private, and the GitHub Actions secret cannot reach it.
- Logs never contain prompt text or emails.

## v1.1 notes (now in scope: merged into the sections above)

Owner direction, 2026-09-29.

- **Two views on `/tokens`: Overview (default) and Detail.**
  - **Overview:**
    - Modelled on GitHub's contribution graph. Weeks run as columns, with a year selector like GitHub's ("Last 12 months", 2026, 2025, … back to the first data), a "N tokens in the last year" style headline, and quiet month and weekday labels.
    - The colours are GitHub's contribution levels (Visual design, v1.5; the thermal ramp is retired).
    - Numbers: total tokens (in + out), with a quiet line showing in, out, the average per month, and the account, provider and live-machine counts.
    - "In" here means all input, including cached input.
    - Nothing else.
  - **Detail** (a toggle):
    - A tokens-over-time chart with Day / Week / Month granularity.
    - Breakdown **by model**, with the **API-equivalent cost** at list prices from `src/data/modelPricing.json`, computed per model from `in, cacheW, cacheR, out`. By default each day is priced at the list price in effect that day (the model's dated `history` windows; a date no window covers uses today's price), with a toggle to price everything at today's list prices.
    - Breakdown **by workstation**: the machine's public label plus a country flag from its system time zone.
    - Breakdown **by region**: country plus flag.
    - The metric and "exact only" toggles live here.
    - Project and workspace names are **never** public.
- **Collector:**
  - The heartbeat gains `tz: {iana, windowsId, country, source}` from `internal/tzinfo` (the API also accepts `cc` for `country`).
  - `install` asks for a public machine label (default: hostname) and warns that it is shown on the public page.
  - A new `label NEW_NAME` command renames the machine.
- **API:**
  - An event row stores `machine` and `cc` from its first reporter. These are never changed by later merges.
  - The rollup gains `models: {model: {in, cacheW, cacheR, out, reasoning, calls}}` per provider (replacing effective-per-model), plus `byMachine: {machineId: {in, cacheW, cacheR, out}}` and `byCountry: {CC: {…}}`.
  - `GET /api/usage` adds a public `machines: [{id, label, cc, live, lastSeenAt}]` list.
- **Flags:** render them as SVG, not emoji, because Windows cannot render flag emoji. Lazy-load them with the Detail view.

## Desktop app (v1.4): ONE app, ONE process, with a tray icon (Windows) / menu-bar item (macOS)

- **Compact heading (v0.2.3):** the first row is `● AC MBP 2`: only the dot is green/red, the machine name stays white. A muted status line below it shows scanning, uploading with the remaining record count, retry/error, or up-to-date and the next sync. There is no machine/fleet footer. Both pinned and unpinned views use `tray.Panel` on Windows and macOS. Local provider totals refresh from scan checkpoints and before uploading, without waiting for a server response. Problems keep the dot red until cleared, including during a retry. Provider rows are sorted by latest event timestamp descending and move as activity changes. For activity older than an hour, the 24h/30d totals use the same muted colour and lighter weight as the age and last-event count.

Owner decision: "one app, not two ... one running process". The collector itself is the tray/menu-bar app.

- **One process.** `tokenmaxr` started with no arguments, or with `app`, on a desktop session runs the tray/menu-bar UI **and** the collection loop in the same process. It runs a tick at start, then every 60 s (every 10 s while the live panel is pinned), plus on demand from the menu, within the same 50 s budget and lock semantics. Every tick uploads what it queued.
  - The lock keeps a single instance. A second launch hands off: it tells the running one to show itself, then exits.
- **Keeping it alive** without a second running process:
  - macOS: a LaunchAgent `com.d0m1.collector` running the app, with RunAtLoad and KeepAlive, so launchd restarts it if it dies.
  - Windows: an HKCU Run value starting `tokenmaxrw.exe` at login. The existing per-user scheduled task becomes a **watchdog**: every 5 minutes it launches the app, which exits immediately if already running because of the lock. So a crash is healed within 5 minutes and there is never more than one running process.
  - The old "task runs `run` every minute" model is retired. `run` stays as a CLI command.
- **The CLI remains the same program.** On Windows the program ships as `tokenmaxrw.exe` (the app: GUI subsystem, no console window) plus `tokenmaxr.exe` for typing commands (`status`, `invite`, …) in a terminal. Windows forces a choice between console and GUI per exe; it is the same code, not a second app. macOS has one binary.
  - macOS builds need cgo, because the menu-bar API is Cocoa, so they are built on a macOS CI runner. Windows stays CGO_ENABLED=0.
- **Library:** `fyne.io/systray` for the icon and the tooltip. On Windows and macOS the app takes both clicks with `systray.SetOnTapped` / `SetOnSecondaryTapped`: with a tap handler set, systray (v1.12) calls it instead of showing its native menu, so no menu item is ever added. Icon updates, the tooltip and re-adding the icon when Explorer restarts (`TaskbarCreated`) stay systray's; no own `Shell_NotifyIconW` layer is needed.
- **Icon: just green or red.**
  - green = the last tick is under 3 minutes old and the last upload succeeded (or there is nothing to send);
  - red = anything else: an upload error, backoff, a rejected token, not enrolled, or stale.
  - Icons are generated in code and legible on light and dark taskbars.
- **Hover tooltip:** the same current activity as the status line, or `tokenmaxr · ERROR: <few words>`.
- **Click menu** (left or right) is the owner-drawn **popup** below, on Windows and on macOS. Minimal, one line per provider:
  - `● AC MBP 2`: green dot when healthy, red when there is a problem. No extra status or identity rows.
  - A separator, then one line per provider seen locally, ordered by latest activity, newest first:
    - `Claude   2 min ago   +67.5K  │   24h 20.1B   30d 13.4B` (owner direction, 2026-09-30)
    - that is, provider, time of the latest event, `+` the tokens that event added, then the rolling 24 h total (the last 24 UTC clock hours, the current one included) and the rolling 30-day total. The hourly column is gone.
    - Tokens = in + cacheW + cacheR + out, as on the site. Every count in the menu and panel uses K/M/B (`Compact`).
    - **Right-aligned totals.** On Windows the totals follow a tab, the menu's shortcut column, so every row's totals start at the same x. Each figure is left-padded to the width of `20.1B` (three digits, a point, a unit) with U+2007 figure spaces and a U+2008 punctuation space, so in the menu font's tabular figures the numbers end in line. macOS shows ` │ ` instead of the tab.
  - A separator, then `Open dashboard` (d0m1.com/tokens), `Pin to screen` (or `Unpin` while pinned), `Sync now`, `Open log`, `Quit`.
  - Quit confirms on its own row (below): the first click arms it, the second quits.
- **Click popup (owner direction 2026-10-01: "make the context menu owner-draw and look like the pinned version", and on macOS "owner draw like on windows; same style/reuse the code").** Clicking the icon, left or right, opens a popup drawn by the pinned panel's renderer. Windows has one shared renderer (`internal/tray/ui/sheet_windows.go`); macOS has one shared sheet view (`D0m1SheetView` in `native_darwin.go`) hosted by both the panel and the popup. The rows, the hit-testing and the click state machine are the pure model (`tray.Popup`, `tray.HoverPopup`, `tray.ClickPopup`), so neither platform keeps a second copy.
  - Rows (`tray.Popup`): the panel's lines (health dot and machine heading, a rule, the provider rows), then a rule and the action rows `Open dashboard`, `Pin to screen` / `Unpin`, `Sync now` (greyed out while a tick runs, labelled `Scanning…`, `Uploading…`, or the matching current phase), `Open log`, `Quit`.
  - Only the action rows are hover-highlighted: a subtle green `#17331f` behind the text, which brightens from `#c9d1d9` to white.
  - Keyboard: Up/Down (and Tab) move the highlight over the action rows, wrapping and skipping a greyed-out one; Enter (or Space) runs it; Esc closes.
  - Quit confirms on its row. The first click turns it into `Click again to quit (stops until next login)` in red; a second click quits (`ActQuitNow`, no dialog). Moving off the row, or 5 s, disarms it. No modal dialog fights the popup for the focus.
  - Position (`tray.PopupPos`, pure):
    - It is anchored to the icon's rectangle from `Shell_NotifyIconGetRect` (systray's window in this process, icon id 100), or to the cursor at click time when that fails.
    - It opens on the work-area side of the taskbar, whichever edge the taskbar is on: the edge the work area is short of, or, for an auto-hidden taskbar, the monitor edge nearest the icon.
    - It sits 8 px (DPI-scaled) from the taskbar, centred on the icon, clamped into that monitor's work area (`MonitorFromRect`/`GetMonitorInfo`).
  - Window: `WS_POPUP` with `WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED` (no taskbar or Alt-Tab entry), per-monitor DPI aware (v2), on its own locked OS thread like the panel. Unlike the panel it takes the focus (`SetForegroundWindow`, `SetFocus`).
  - It closes on focus loss (a click outside, Alt-Tab), on Esc, on its `×`, on a second click on the icon, and after an action. The icon click is a toggle: the click that took the focus from the popup does not reopen it. If the shell did not let the popup take the foreground, a mouse button pressed outside it closes it.
  - While it is open the app redraws every second, so the relative times move.
  - It is independent of the pinned panel: two windows, and both can be open.
  - **macOS** uses that same sheet view in a borderless key `NSPanel` at popup level (`popup_darwin.go`). It is anchored with `tray.PopupPos` after flipping the menu-bar item's y-up frame (`tray.FlipDown`), or the cursor when the item has no window. It takes the key so Esc and the arrow keys work, and it closes when it resigns key, on a click in another of this app's windows, on Esc, on its `×`, on the icon again, and after an action. The icon click that closed it does not reopen it.
- **Pinned live panel.** `Pin to screen` opens a small borderless floating window that stays on top, for watching the counts move while working. It shows the status line, the provider rows and the machine/fleet line (no actions). It is laid out in a monospace font, so the pure model (`tray.Panel`, `tray.Align`) pads the columns with spaces:
  - `Claude   2 min ago   +67.5K  │   24h 20.1B   30d 13.4B`, with names and ages left-aligned and the `+` value and the totals right-aligned.
  - The `+` value is drawn in the accent green on Windows. On macOS the age and the `+` value stay the line's ink (`#f0f6fc`).
  - An event older than an hour draws its age and its `+` value in the muted grey `#9198a1` at a lighter weight, on both. Within the hour they stay full weight, so a recent row jumps out. The name and the 24 h / 30 d totals stay full ink either way.
  - Look: the site's dark sheet: a near-black green pane, a grey-and-green double border, `#f0f6fc` ink, `#9198a1` muted text, `#56d364` for OK and the `+` values, `#f85149` for errors.
  - While pinned, the app ticks (and uploads) every 10 s instead of 60 s. Pinning ticks at once. The panel redraws every second so the relative times move. Unpinned, it goes back to 60 s, and the menu redraws every 10 s (every second while the popup is open).
  - The panel can be dragged anywhere and never takes the focus. `Unpin`, or the small `×` on the panel, closes it.
  - `config.json` remembers the state as `panel: {pinned, x, y, placed}`. A pinned panel comes back pinned, at the same spot, after a restart or a login.
    - The app saves it under `collector.lock`, so a tick's own config save cannot lose it, and it never overwrites an invalid config.json.
    - x, y is the top-left corner: physical pixels on Windows, points (y up) on macOS.
    - Until the panel is first moved it sits in a corner of the work area: bottom-right on Windows, top-right on macOS. A saved spot is clamped onto the nearest monitor.
  - **Windows:** pure Go Win32 (`internal/tray/ui/panel_windows.go`, no cgo). `CreateWindowEx` with `WS_POPUP` and `WS_EX_TOPMOST | WS_EX_TOOLWINDOW | WS_EX_LAYERED | WS_EX_NOACTIVATE`, so it stays off the taskbar and Alt-Tab and never steals the focus.
    - It runs its own message loop on a locked OS thread, apart from systray's. Updates are posted to it.
    - It is per-monitor DPI aware (v2) and handles `WM_DPICHANGED`.
    - It is drawn with double-buffered GDI in Consolas. `WM_NCHITTEST` returns `HTCAPTION` (drag) everywhere except the `×`. Windows 11 rounds its corners.
  - **macOS:** a borderless, non-activating `NSPanel` at the floating level, on all Spaces and outside the window cycle, with a monospaced system font, in the cgo file (`native_darwin.go`, with its `//export` callbacks in `panel_darwin.go`). It is built only on the macOS runner.
  - Diagnostics: the running app also takes `app.pin`, `app.unpin` and `app.dump` flag files, like `app.quit`. `app.dump` writes the menu, panel and popup text to `app.view.txt`, with the UI's own state: the panel and popup windows' styles (topmost, tool window, …), position, DPI and focus, the popup's anchor, highlighted row and why it last closed. Display text only, never the token or K.
- **Local numbers.** An in-memory rolling window (last 30 days) per provider, persisted compactly in `state.json` as `recent: {provider: {lastTs, lastTokens, hourly: [...], daily: [...]}}` (hourly feeds the 24 h total, daily the 30 d one). It is merged by event id with fieldwise max, the same invariant as the server, over a bounded id set, so re-reads never double count. There is no stats.json and no report.html. Projects and models are not shown locally.
- **Install and uninstall.**
  - `install` registers the app autostart (and the Windows watchdog task) and starts the app. `--no-app` falls back to the old headless mode: the scheduled `run` every minute, for servers without a desktop.
  - `uninstall` quits the app and removes both.
  - Self-update swaps the binary and the app re-execs itself.
- **Release.** The darwin artifacts (`darwin-arm64`, `darwin-amd64`) are built with cgo on a macos-14 runner and handed to the publish job. There are no new manifest keys.
