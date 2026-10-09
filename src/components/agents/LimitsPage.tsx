import { useEffect, useMemo, useRef, useState, type CSSProperties } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import LoadingIndicator from '../LoadingIndicator'
import { accountName, accountTrackingKey, ago, buildAccounts, countdownParts, initializeTracking, LimitsDenied, mockLimits, parsePlanOverrides, parseTracking, resetSeverity, severityColor, trackedChoice, weeklyMeter, type Account, type AccountTracking, type LimitRow, type Meter } from './limits'
import TokensShell from './TokensShell'
import AuthGate from './AuthGate'
import { LOCAL_PREFS, useTokensSite, type SitePrefs } from './site'
import { useUsageSource } from './source'
import './agents-base.css'
import './LimitsPage.css'

/** Meters change slowly; the collector rereads them every few minutes. */
const REFRESH_MS = 60_000
const TRACKING_KEY = 'd0m1.agents.tracking.v1'
const PLAN_LABELS_KEY = 'd0m1.agents.plan-labels.v1'

function usePlanLabels(prefs: SitePrefs) {
  const [labels, setLabels] = useState(() => {
    try { return parsePlanOverrides(prefs.get(PLAN_LABELS_KEY)) } catch { return {} }
  })
  useEffect(() => prefs.subscribe(PLAN_LABELS_KEY, value => setLabels(parsePlanOverrides(value))), [prefs])
  return labels
}

function readTracking(prefs: SitePrefs, key: string): AccountTracking {
  try { return parseTracking(prefs.get(key)) } catch { return {} }
}

/** Remember every initial choice, so a meter expiring never moves an account out of view. */
function useTracking(accounts: Account[], mock: boolean, prefs: SitePrefs) {
  const key = `${TRACKING_KEY}${mock ? '.mock' : ''}`
  const [stored, setStored] = useState(() => ({ key, choices: readTracking(prefs, key) }))
  const [saveFailed, setSaveFailed] = useState(false)
  const choices = stored.key === key ? stored.choices : readTracking(prefs, key)
  useEffect(() => {
    setStored(previous => {
      const original = previous.key === key ? previous.choices : readTracking(prefs, key)
      const next = initializeTracking(accounts, original)
      return previous.key === key && next === original ? previous : { key, choices: next }
    })
  }, [accounts, key, prefs])
  useEffect(() => {
    if (stored.key !== key) return
    try {
      prefs.set(key, JSON.stringify(stored.choices))
      setSaveFailed(false)
    } catch { setSaveFailed(true) }
  }, [key, stored, prefs])
  useEffect(() => prefs.subscribe(key, value => setStored({ key, choices: parseTracking(value) })), [key, prefs])
  const setTracked = (account: Account, tracked: boolean) => setStored(previous => ({
    key,
    choices: { ...(previous.key === key ? previous.choices : readTracking(prefs, key)), [accountTrackingKey(account)]: tracked },
  }))
  return { choices, setTracked, saveFailed }
}

type Load =
  | { status: 'loading' }
  | { status: 'ok'; items: LimitRow[]; at: number }
  | { status: 'unauthorized' }
  | { status: 'forbidden' }
  | { status: 'error'; message: string; items?: LimitRow[]; at?: number }

/** A one-second clock for the countdowns. */
function useNow(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  return now
}

/** The owner's limits from the page's source (d0m1.com: GET /api/limits; GitHub Pages: the decrypted owner files). */
function useLimits(mock: boolean, reloadKey: number, authKey: string) {
  const source = useUsageSource()
  const site = useTokensSite()
  const onDenied = useRef(site.onDenied)
  onDenied.current = site.onDenied
  const [load, setLoad] = useState<Load>({ status: 'loading' })
  const [refreshing, setRefreshing] = useState(false)
  useEffect(() => {
    if (mock) {
      setLoad({ status: 'ok', items: mockLimits(Date.now()), at: Date.now() })
      setRefreshing(false)
      return
    }
    const fetchLimits = source.fetchLimits
    if (!fetchLimits) {
      setLoad({ status: 'unauthorized' })
      return
    }
    let cancelled = false
    let controller: AbortController | null = null
    const read = () => {
      if (controller) return
      controller = new AbortController()
      setRefreshing(true)
      fetchLimits(AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]))
        .then((items) => {
          if (!cancelled) setLoad({ status: 'ok', items, at: Date.now() })
        })
        .catch((err: unknown) => {
          if (cancelled) return
          if (err instanceof LimitsDenied) {
            onDenied.current?.(err.status)
            return setLoad({ status: err.status === 401 ? 'unauthorized' : 'forbidden' })
          }
          const message = err instanceof Error && err.name === 'TimeoutError' ? 'Reading the weekly quota timed out. Try again shortly.'
            : err instanceof Error ? err.message : 'Could not read the limits.'
          // Keep the last good meters on screen under the notice.
          setLoad((prev) => (prev.status === 'ok' || prev.status === 'error' ? { status: 'error', message, items: prev.items, at: prev.at } : { status: 'error', message }))
        })
        .finally(() => {
          controller = null
          if (!cancelled) setRefreshing(false)
        })
    }
    read()
    const timer = window.setInterval(read, REFRESH_MS)
    return () => {
      cancelled = true
      window.clearInterval(timer)
      controller?.abort()
    }
  }, [mock, reloadKey, authKey, source])
  return { load, refreshing }
}

const RESET_DATE = new Intl.DateTimeFormat('en-GB', {
  weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit',
})
const FULL_DATE = new Intl.DateTimeFormat('en-GB', {
  dateStyle: 'medium', timeStyle: 'long',
})
const TIME_ZONE = Intl.DateTimeFormat().resolvedOptions().timeZone

/** Source percentages are used; the gauge deliberately shows the quota still available. */
function WeeklyBar({ meter, label }: { meter: Meter; label: string }) {
  const available = meter.full ? 0 : 100 - Math.min(100, Math.max(0, meter.used ?? 0))
  const percent = Math.round(available)
  const style = { '--meter-color': severityColor(1 - available / 100) } as CSSProperties
  return (
    <div className="limits-bar" style={style} title={`${percent}% of the weekly quota available`}>
      <span className="limits-bar__pct">{percent}<span>%</span></span>
      <div className="limits-bar__track" role="progressbar" aria-label={`${label}: weekly quota available`}
        aria-valuemin={0} aria-valuemax={100} aria-valuenow={percent} aria-valuetext={`${percent}% available`}>
        <span className="limits-bar__fill" style={{ width: `${available}%` }} />
      </div>
    </div>
  )
}

function ResetCountdown({ meter, now }: { meter: Meter; now: number }) {
  const severity = resetSeverity(meter.resetsAt, now)
  if (meter.resetsAt === null || severity === null) return <div className="limits-reset limits-reset--unknown">Reset time unavailable</div>
  const parts = countdownParts(meter.resetsAt - now)
  // Tool-displayed reset times have minute precision; avoid displaying :59 from parse-time drift.
  const resetDate = new Date(Math.round(meter.resetsAt / 60_000) * 60_000)
  return (
    <div className="limits-reset" style={{ '--reset-color': severityColor(severity) } as CSSProperties}>
      <span className="limits-reset__label">resets in</span>
      <span className="limits-reset__countdown" aria-label={parts.map(p => `${p.value} ${p.unit}${p.value === 1 ? '' : 's'}`).join(' ')}>
        {parts.map(part => <span className="limits-reset__part" key={part.unit}>
          <strong>{part.value}</strong><span>{part.unit[0]}</span>
        </span>)}
      </span>
      <time className="limits-reset__date" dateTime={new Date(meter.resetsAt).toISOString()} title={FULL_DATE.format(resetDate)}>
        {RESET_DATE.format(resetDate)}
      </time>
    </div>
  )
}

/** A Team or Enterprise organisation's name, or any organisation's when there is no plan: with the plan, it tells
 * apart two accounts of one email (a Team seat and a personal plan). A personal organisation's name is the person's. */
function OrgName({ account }: { account: Account }) {
  const named = account.orgKind === 'team' || account.orgKind === 'enterprise' || !account.plan
  return account.org && named ? <span className="limits-line__org">{account.org}</span> : null
}

/** One dense line, like Other's: identity and its notes, the available-quota
 * gauge, then the weekly reset countdown. */
function AccountLine({ account, now, onTrack }: { account: Account; now: number; onTrack: (account: Account, tracked: boolean) => void }) {
  const week = weeklyMeter(account)
  const observedAt = week?.observedAt ?? account.observedAt
  const oldReading = now - observedAt >= 86_400_000
  return (
    <li className={`limits-line${week?.full ? ' is-full' : ''}`}>
      <div className="limits-line__who">
        <label className="limits-track">
          <input type="checkbox" checked onChange={() => onTrack(account, false)} aria-label={`Track ${account.tool} ${account.label ? accountName(account) : 'unknown account'}`} />
          <span className="limits-line__email" title={accountName(account)}>{account.email || 'Unknown account'}</span>
        </label>
        <span className="limits-plan">{account.plan || 'Plan unavailable'}</span>
        <OrgName account={account} />
        <span className={`limits-reading${oldReading ? ' is-old' : ''}`} title={`Reading from ${FULL_DATE.format(new Date(observedAt))}`}>
          read {ago(observedAt, now)}
        </span>
        {account.blockedBy?.window === 'session' && <span className="limits-session-notice">Temporarily limited</span>}
        {week?.detail && <span className="limits-line__detail">{week.detail}</span>}
      </div>
      {week ? <>
        <WeeklyBar meter={week} label={accountName(account)} />
        <ResetCountdown meter={week} now={now} />
      </> : <div className="limits-no-reading">{account.week?.lapsed ? 'Reset passed · awaiting a new reading' : 'No weekly reading available'}</div>}
    </li>
  )
}

/** One box per tool (Claude, Codex, …), its accounts one line each. */
function ProviderBox({ tool, accounts, now, onTrack }: { tool: string; accounts: Account[]; now: number; onTrack: (account: Account, tracked: boolean) => void }) {
  return (
    <section className="limits-box" aria-label={tool}>
      <header className="limits-box__head">
        <h2 className="limits-box__title">{tool}</h2>
        <span className="limits-box__count">
          {accounts.length === 1 ? '1 account' : `${accounts.length} accounts`}
        </span>
      </header>
      <ul className="limits-lines">
        {accounts.map((account) => (
          <AccountLine key={account.key} account={account} now={now} onTrack={onTrack} />
        ))}
      </ul>
    </section>
  )
}

function OtherAccounts({ accounts, onTrack }: { accounts: Account[]; onTrack: (account: Account, tracked: boolean) => void }) {
  return (
    <details className="limits-unmetered">
      <summary>Other <span className="limits-unmetered__count">{accounts.length}</span></summary>
      <p className="limits-unmetered__hint">Tick an account to track it above.</p>
      <ul className="limits-lines">
        {accounts.map(account => <li className="limits-unmetered__line" key={account.key}>
          <label className="limits-track">
            <input type="checkbox" checked={false} onChange={() => onTrack(account, true)} aria-label={`Track ${account.tool} ${account.label ? accountName(account) : 'unknown account'}`} />
            <span className="limits-unmetered__tool">{account.tool}</span>
            <span className="limits-line__email" title={accountName(account)}>{account.email || 'Unknown account'}</span>
          </label>
          <span className="limits-plan">{account.plan || 'Plan unavailable'}</span>
          <OrgName account={account} />
          <span className="limits-unmetered__reason">{weeklyMeter(account) ? 'Not tracked' : account.week?.lapsed ? 'Awaiting a new reading' : 'No weekly reading'}</span>
        </li>)}
      </ul>
    </details>
  )
}

const LimitsPage = () => {
  const [searchParams] = useSearchParams()
  const mock = searchParams.get('mock') === '1'
  const site = useTokensSite()
  const owner = site.useOwner()
  const [reloadKey, setReloadKey] = useState(0)
  const { load, refreshing } = useLimits(mock, reloadKey, owner.key)

  const items = load.status === 'ok' || load.status === 'error' ? load.items : undefined
  const now = useNow()
  const prefs = site.prefs ?? LOCAL_PREFS
  const planLabels = usePlanLabels(prefs)
  const accounts = useMemo(() => (items ? buildAccounts(items, now, planLabels) : []), [items, now, planLabels])
  const { choices, setTracked, saveFailed } = useTracking(accounts, mock, prefs)
  const isTracked = (account: Account) => trackedChoice(choices, account)
  const tracked = accounts.filter(isTracked)
  const other = accounts.filter(account => !isTracked(account))
  // Accounts arrive in provider order, so the boxes keep it.
  const byTool = useMemo(() => {
    const groups = new Map<string, Account[]>()
    for (const a of accounts.filter(account => trackedChoice(choices, account))) groups.set(a.tool, [...(groups.get(a.tool) ?? []), a])
    return [...groups]
  }, [accounts, choices])

  const crumbs = [{ label: 'tokens', to: site.home }, { label: 'agents' }]
  const gate = site.agentsGate
  const forbidden = load.status === 'forbidden' && owner.status !== 'visitor'
  if (owner.status === 'visitor' || load.status === 'unauthorized' || load.status === 'forbidden') return (
    <TokensShell crumbs={crumbs}>
      <AuthGate title={forbidden ? gate.forbiddenTitle : gate.title} description={gate.description} forbidden={forbidden} hint={gate.hint}>
        {gate.SignIn && !forbidden ? <gate.SignIn /> : undefined}
      </AuthGate>
    </TokensShell>
  )

  return (
    <TokensShell crumbs={crumbs} className="agents-gh limits-page">
      {mock && (
        <div className="agents-testdata" role="status">
          Test data: every account on this page is synthetic, for previewing the layout. None of it is real.
        </div>
      )}

      <div className={`${site.sheet ? 'agents-panel ' : ''}agents-sheet`}>
        <div className="agents-sheet__scroll">
          {(load.status === 'loading' || (refreshing && !items)) && <LoadingIndicator label={owner.status === 'checking' ? 'Checking your sign-in…' : 'Loading weekly quota…'} />}

          {items && (
            <>
              <div className="limits-top">
                <div aria-live="polite">
                  <h1 className="limits-headline">
                    Weekly quota
                  </h1>
                  <p className="limits-sub">
                    {tracked.length} {tracked.length === 1 ? 'account' : 'accounts'} tracked · 100% available is a full quota
                  </p>
                </div>
                <button type="button" className="agents-btn" disabled={refreshing} onClick={() => setReloadKey((key) => key + 1)}>
                  {refreshing ? <LoadingIndicator label="Refreshing…" inline /> : 'Refresh'}
                </button>
              </div>

              {load.status === 'error' && (
                <div className="agents-notice" role="alert">
                  <p>Showing the last good meters. {load.message}</p>
                </div>
              )}
              {saveFailed && <p className="agents-notice" role="status">Your selection could not be saved in this browser.</p>}
              {accounts.length === 0 ? (
                <p className="agents-empty">No accounts yet. The collector reports each signed-in tool’s plan and meters.</p>
              ) : (
                <div className="limits-boxes">
                  {byTool.map(([tool, list]) => (
                    <ProviderBox key={tool} tool={tool} accounts={list} now={now} onTrack={setTracked} />
                  ))}
                </div>
              )}
              {accounts.length > 0 && tracked.length === 0 && <p className="limits-sub">Choose accounts from Other to track their weekly quota.</p>}
              {other.length > 0 && <OtherAccounts accounts={other} onTrack={setTracked} />}
            </>
          )}

          {load.status === 'error' && !items && !refreshing && (
            <div className="agents-notice" role="alert">
              <p>{load.message}</p>
              <button type="button" className="agents-btn" onClick={() => setReloadKey((key) => key + 1)}>
                Try again
              </button>
            </div>
          )}
        </div>
      </div>

      <footer className="agents-foot">
        <span>Latest reported weekly usage · reset times in {TIME_ZONE} · selection saved in this browser</span>
        <span> · </span>
        <Link to={site.home}>Token usage</Link>
      </footer>
    </TokensShell>
  )
}

export default LimitsPage
