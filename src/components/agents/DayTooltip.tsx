import { averageSources, projectLabel, samplePeriod } from './tooltip'
import UsageBar from './UsageBar'
import {
  ACCOUNT_HISTORY, formatCompact, formatDayShort, modelLabel, monthShort, PROVIDER_LABEL, PROVIDERS,
  type DayCell, type DayDerivation, type ProjectCell,
} from './usage'

const count = (n: number, singular: string) => `${formatCompact(n)} ${singular}${n === 1 ? '' : 's'}`

function Counts({ tokens, prompts, noun = 'tokens', inferred = false, archived = false }: {
  tokens: number | null; prompts: number | null; noun?: string; inferred?: boolean; archived?: boolean
}) {
  return <span className="agents-day-tip__counts">
    <span><strong>{tokens === null ? '—' : `${inferred ? '≈' : ''}${formatCompact(tokens)}`}</strong> <span className="agents-day-tip__unit">{noun}</span></span>
    <span className="agents-day-tip__separator" aria-hidden="true">·</span>
    <span title={prompts === null ? 'Prompt count unavailable' : archived ? 'Archived prompts attributed to this model. Totals include all observed prompts.' : undefined}><strong>{prompts === null ? '—' : formatCompact(prompts)}</strong> <span className="agents-day-tip__unit">{prompts === 1 ? 'prompt' : 'prompts'}</span></span>
  </span>
}

function split(total: number, inferred: number) {
  const recorded = Math.max(0, total - inferred)
  if (inferred <= 0) return `${formatCompact(recorded)} recorded`
  if (recorded <= 0) return `${formatCompact(inferred)} estimated`
  return `${formatCompact(recorded)} recorded + ${formatCompact(inferred)} estimated`
}

function ProjectRow({ project, derivations, noun }: { project: ProjectCell; derivations: DayDerivation[]; noun: string }) {
  const missing = project.promptsNoUsage > 0 && project.inferred <= 0
  const allInferred = project.inferred > 0 && project.total - project.inferred < 0.01 && derivations.length > 0
  const basis = derivations[0]?.basis.startsWith('workspace') ? 'project average' : 'provider average'
  return (
    <li className="agents-day-tip__project">
      <div className="agents-day-tip__row">
        <span>{projectLabel(project.workspace)}{derivations.length > 0 && <span className="agents-day-tip__basis"> · {basis}</span>}</span>
        <Counts tokens={project.total <= 0 && missing ? null : project.total}
          prompts={project.workspace === ACCOUNT_HISTORY ? null : project.prompts} noun={noun} inferred={project.inferred > 0} />
      </div>
      {project.inferred > 0 && !allInferred && (
        <p className="agents-day-tip__secondary">{split(project.total, project.inferred)}</p>
      )}
      {missing && <p className="agents-day-tip__secondary">{count(project.promptsNoUsage, 'prompt')} without token records</p>}
      {project.workspace === ACCOUNT_HISTORY && <p className="agents-day-tip__secondary">Provider daily total · project and machine unknown</p>}
      {derivations.map((d, i) => (
        <div className="agents-day-tip__calculation" key={i}>
          <p><span className="agents-day-tip__secondary">{monthShort(Number(d.month.slice(5, 7)) - 1)}: </span>{formatCompact(d.missingInMonth)} missing × {formatCompact(d.rate)} ÷ {count(d.affectedDays, 'day')} ≈ <strong>{formatCompact(d.dailyEstimate)}/day</strong></p>
        </div>
      ))}
    </li>
  )
}

/** The same provider/project hierarchy for recorded, estimated and mixed days. */
export default function DayTooltip({ cell, date, today, noun, cost }: {
  cell?: DayCell; date: string; today: string; noun: string; cost: string | null
}) {
  const sources = averageSources(cell?.derivations ?? [])
  const providers = PROVIDERS.filter(provider => {
    const p = cell?.byProvider[provider]
    return p && (p.total > 0 || p.prompts > 0)
  }).sort((a, b) => cell!.byProvider[b]!.total - cell!.byProvider[a]!.total)
  const models = cell?.models.filter(([model, value, provider]) => value >= 0.5 || (cell.modelPrompts?.[`${provider}|${model}`] ?? 0) > 0) ?? []
  const modelPrompts = (provider: string, model: string) => model === ACCOUNT_HISTORY ? null : cell?.modelPrompts?.[`${provider}|${model}`] ?? null
  return (
    <div className="agents-day-tip">
      <p className="agents-day-tip__heading">
        <span>{formatDayShort(date, today)}</span>
        <Counts tokens={cell?.total ?? 0} prompts={cell?.prompts ?? 0} noun={noun} inferred={(cell?.inferred ?? 0) > 0} />
      </p>
      {(cell?.inferred ?? 0) > 0 && <p className="agents-day-tip__secondary">{split(cell?.total ?? 0, cell?.inferred ?? 0)}</p>}
      {cell && (
        <p className="agents-day-tip__secondary agents-day-tip__meta">
          {[
            cell.tokensIn + cell.tokensOut > 0 && `in ${formatCompact(cell.tokensIn)} · out ${formatCompact(cell.tokensOut)}`,
            cell.unattributed > 0 && `${formatCompact(cell.unattributed)} account history`,
            cost,
          ].filter(Boolean).join(' · ')}
        </p>
      )}
      {providers.map(provider => {
        const p = cell!.byProvider[provider]!
        return (
          <section className="agents-day-tip__provider" key={provider} aria-label={PROVIDER_LABEL[provider]}>
            <div className="agents-day-tip__row agents-day-tip__provider-heading">
              <span><span className={`agents-key agents-key--${provider}`} aria-hidden="true" />{PROVIDER_LABEL[provider]}</span>
              <Counts tokens={p.total <= 0 && p.promptsNoUsage > 0 ? null : p.total} prompts={p.prompts} noun={noun} inferred={p.inferred > 0} />
            </div>
            <ul className="agents-day-tip__projects">
              {p.projects.map(project => (
                <ProjectRow key={project.workspace ?? 'unknown'} project={project} noun={noun}
                  derivations={(cell?.derivations ?? []).filter(d => d.provider === provider && d.workspace === project.workspace)} />
              ))}
            </ul>
          </section>
        )
      })}
      {sources.length > 0 && (
        <details className="agents-day-tip__details">
          <summary>Average sources ({sources.length})</summary>
          {sources.map((d, i) => (
            <div className="agents-day-tip__source" key={i}>
              <p>{PROVIDER_LABEL[d.provider]} · {d.basis.startsWith('workspace') ? projectLabel(d.workspace) : 'Provider average'}</p>
              <p className="agents-day-tip__secondary">{samplePeriod(d.sampleFrom, d.sampleTo)}</p>
              <p>{formatCompact(d.rate * d.samplePrompts)} {noun} ÷ {count(d.samplePrompts, 'prompt')} ≈ {formatCompact(d.rate)}/prompt</p>
            </div>
          ))}
          {sources.some(d => d.basis.startsWith('provider')) && <p className="agents-day-tip__secondary">Project averages need at least 30 recorded prompts.</p>}
        </details>
      )}
      {models.length > 0 && (
        <section className="agents-day-tip__details" aria-label="Models">
          <p className="agents-day-tip__secondary">{models.some(([model]) => model === ACCOUNT_HISTORY) ? 'Models & account history' : `Models (${models.length})`}</p>
          <ul className="agents-tooltip__rows agents-day-tip__models">
            {models.map(([model, value, provider]) => (
              <li key={`${provider}|${model}`}>
                <span className={`agents-key agents-key--${provider}`} aria-hidden="true" />
                <span className="agents-tooltip__row-label">{modelLabel(model)}</span>
                <Counts tokens={value} prompts={modelPrompts(provider, model)} noun={noun} archived />
                <UsageBar share={cell && cell.total > 0 ? value / cell.total : 0} lead={value === models[0][1]} />
              </li>
            ))}
          </ul>
          {models.some(([model, , provider]) => modelPrompts(provider, model) === null) && <p className="agents-day-tip__secondary agents-day-tip__unavailable">— prompt count unavailable</p>}
        </section>
      )}
    </div>
  )
}
