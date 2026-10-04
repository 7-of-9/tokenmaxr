import { useCallback, useLayoutEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'

interface ChartTooltipProps {
  id: string
  /** Viewport rect of the hovered or focused mark. */
  anchor: DOMRect
  children: ReactNode
  interactive?: boolean
}

const MARGIN = 8

/** Always opens below its mark; long content scrolls within the space below the tile. */
const ChartTooltip = ({ id, anchor, children, interactive = false }: ChartTooltipProps) => {
  const ref = useRef<HTMLDivElement>(null)
  const [position, setPosition] = useState<{ left: number; top: number; maxHeight: number } | null>(null)

  const measure = useCallback(() => {
    const el = ref.current
    if (!el) return
    const { width } = el.getBoundingClientRect()
    const viewportWidth = document.documentElement.clientWidth
    const centre = anchor.left + anchor.width / 2
    const left = Math.min(Math.max(MARGIN, centre - width / 2), Math.max(MARGIN, viewportWidth - width - MARGIN))
    const top = Math.max(MARGIN, anchor.bottom + MARGIN)
    const maxHeight = Math.max(0, Math.min(640, window.innerHeight * 0.7, window.innerHeight - top - MARGIN))
    setPosition(previous => previous?.left === left && previous.top === top && previous.maxHeight === maxHeight
      ? previous
      : { left, top, maxHeight })
  }, [anchor])

  useLayoutEffect(() => {
    const el = ref.current
    if (!el) return
    measure()
    // Native details can expand without a React render.
    const observer = new ResizeObserver(measure)
    observer.observe(el)
    el.addEventListener('toggle', measure, true)
    return () => {
      observer.disconnect()
      el.removeEventListener('toggle', measure, true)
    }
  }, [measure, children])

  // Portal to <body> so route transitions (transforms) never offset the fixed position.
  return createPortal(
    <div
      ref={ref}
      id={id}
      role="tooltip"
      className={`agents-tooltip${interactive ? ' agents-tooltip--interactive' : ''}`}
      style={position
        ? { ...position, visibility: position.maxHeight > 0 ? undefined : 'hidden' }
        : { left: -9999, top: 0, visibility: 'hidden' }}
    >
      {children}
    </div>,
    document.body,
  )
}

export default ChartTooltip
