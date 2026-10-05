import { useEffect, useState, type ReactNode } from 'react'
import { useLocation, useNavigationType } from 'react-router-dom'
import {
  readZoomOrigin,
  resolveEnterDir,
  sceneKey,
  type EnterDir,
} from '../utils/motion'
import './PageTransitions.css'

/**
 * The page transitions: each new scene slides (or zooms) in, as utils/motion decides from where it came from.
 * Shared by d0m1.com (App.tsx) and the tokenmaxr GitHub Pages dashboard (pages/dashboard/Dashboard.tsx), so
 * /tokens and /tokens/agents move between each other the same way on both. Reduced motion: PageTransitions.css.
 */
export default function RouteStage({ children }: { children: ReactNode }) {
  const location = useLocation()
  const navType = useNavigationType()
  const scene = sceneKey(location.pathname, location.search)
  const [anim, setAnim] = useState<{ path: string; dir: EnterDir }>({
    path: scene,
    dir: 'none',
  })

  if (scene !== anim.path) {
    setAnim({
      path: scene,
      dir: resolveEnterDir(anim.path, scene, navType, false),
    })
  }

  const zooming = anim.dir === 'zoom-in' || anim.dir === 'zoom-out'
  const [enter, setEnter] = useState<EnterDir>('none')

  useEffect(() => {
    if (anim.dir === 'none') {
      setEnter('none')
      return
    }
    setEnter('none')
    const frame = requestAnimationFrame(() => {
      requestAnimationFrame(() => setEnter(anim.dir))
    })
    return () => cancelAnimationFrame(frame)
  }, [anim.path, anim.dir])

  return (
    <div
      key={anim.path}
      className="route-stage"
      data-enter={enter}
      data-nav={anim.dir}
      style={zooming ? { transformOrigin: readZoomOrigin() } : undefined}
      onAnimationEnd={(event) => {
        if (event.target !== event.currentTarget) return
        if (enter === 'none') return
        setEnter('none')
        setAnim((prev) => (prev.path === anim.path ? { ...prev, dir: 'none' } : prev))
      }}
    >
      {children}
    </div>
  )
}
