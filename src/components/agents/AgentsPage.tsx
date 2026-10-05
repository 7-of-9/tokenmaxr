import { useSearchParams } from 'react-router-dom'
import { useTokensSite } from './site'
import TokensShell from './TokensShell'
import TokensView from './TokensView'

/**
 * d0m1.com/tokens, and the GitHub Pages dashboard's #/: the shared page body (TokensView) in the shared shell.
 * The UsageSource and TokensSite in context say where the data comes from and which site this is.
 */
const AgentsPage = () => {
  const [searchParams] = useSearchParams()
  const { home } = useTokensSite()
  const view = searchParams.get('view') === 'detail' ? 'detail' : 'overview'
  return (
    <TokensShell crumbs={[{ label: 'tokens', to: home }]} className={`agents-gh agents-page--${view}`} pinned>
      <TokensView />
    </TokensShell>
  )
}

export default AgentsPage
