/** A row's share of the total, with the largest row in the emphasis green. */
export default function UsageBar({ share, lead }: { share: number; lead: boolean }) {
  return (
    <span className="gh-bar" aria-hidden="true">
      {share > 0 && <span className={`gh-bar__fill${lead ? ' is-lead' : ''}`} style={{ width: `${Math.min(100, Math.max(1.5, share * 100))}%` }} />}
    </span>
  )
}
