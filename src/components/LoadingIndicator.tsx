import type { ReactNode } from 'react'
import './LoadingIndicator.css'

/** The Projects loading ring, scoped so other pages' global .spinner rules cannot recolor it. */
export default function LoadingIndicator({ label = 'Loading…', detail, inline = false }: {
  label?: string; detail?: ReactNode; inline?: boolean
}) {
  return (
    <span className={`site-loading${inline ? ' site-loading--inline' : ''}`} role="status">
      <span className="site-loading__spinner" aria-hidden="true" />
      <span className="site-loading__copy">
        <span className="site-loading__label">{label}</span>
        {detail && <span className="site-loading__detail">{detail}</span>}
      </span>
    </span>
  )
}
