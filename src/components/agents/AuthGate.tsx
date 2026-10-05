interface AuthGateProps {
  title: string
  description: string
  forbidden?: boolean
  /** How to get in, in place of the sign-in advice (the GitHub Pages dashboard is unlocked, not signed in to). */
  hint?: string
}

export default function AuthGate({ title, description, forbidden = false, hint }: AuthGateProps) {
  return (
    <section className="agents-panel agents-auth-gate">
      <h2>{title}</h2>
      <p className="agents-muted">{description}</p>
      <p className="agents-muted">{hint ?? (forbidden ? 'Use Sign out in the header, then sign in with the owner account.' : 'Use Sign in with GitHub in the header to continue.')}</p>
    </section>
  )
}
