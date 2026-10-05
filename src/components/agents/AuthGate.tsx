import type { ReactNode } from 'react'

interface AuthGateProps {
  title: string
  description: string
  forbidden?: boolean
  /** How to get in, in place of the sign-in advice. */
  hint?: string
  /** The sign-in itself (the GitHub Pages dashboard's), in place of the hint. */
  children?: ReactNode
}

export default function AuthGate({ title, description, forbidden = false, hint, children }: AuthGateProps) {
  return (
    <section className="agents-panel agents-auth-gate">
      <h2>{title}</h2>
      <p className="agents-muted">{description}</p>
      {children ?? <p className="agents-muted">{hint ?? (forbidden ? 'Use Sign out in the header, then sign in with the owner account.' : 'Use Sign in with GitHub in the header to continue.')}</p>}
    </section>
  )
}
