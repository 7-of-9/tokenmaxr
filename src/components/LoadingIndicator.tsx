import type { ReactNode } from 'react'
import './LoadingIndicator.css'

/** The Projects loading ring, scoped so other pages' global .spinner rules cannot recolor it. */
export default function LoadingIndicator({ label = 'Loading…', detail, inline = false, quiet = false }: {
  /** quiet keeps the label for screen readers and shows only the ring. */
  label?: string; detail?: ReactNode; inline?: boolean; quiet?: boolean
}) {
  return (
    <span className={`site-loading${inline ? ' site-loading--inline' : ''}`} role="status">
      <span className="site-loading__spinner" aria-hidden="true" />
      <span className="site-loading__copy">
        <span className={`site-loading__label${quiet ? ' site-loading__label--quiet' : ''}`}>{label}</span>
        {detail && <span className="site-loading__detail">{detail}</span>}
      </span>
    </span>
  )
}
