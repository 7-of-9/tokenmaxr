// d0m1.com's animated background and its controls along the bottom edge, as App.tsx mounts them, streamed from
// d0m1.com's CDN. Opt-in per dashboard ("background": true in tokenmaxr.json): Dashboard.tsx loads this only then.
import BackgroundVideo from '../../src/components/BackgroundVideo'
import UnifiedBackgroundControls from '../../src/components/UnifiedBackgroundControls'
import { useBackgroundTheme } from '../../src/contexts/BackgroundThemeContext'

export default function Background() {
  const { backgroundEnabled } = useBackgroundTheme()
  return (
    <>
      <BackgroundVideo enabled={backgroundEnabled} />
      <UnifiedBackgroundControls />
    </>
  )
}
