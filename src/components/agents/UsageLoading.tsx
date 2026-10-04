import { useEffect, useState } from 'react'
import LoadingIndicator from '../LoadingIndicator'
import type { UsageState } from './useUsage'

function bytes(value: number) {
  return value < 1024 * 1024 ? `${Math.round(value / 1024)} KB` : `${(value / (1024 * 1024)).toFixed(1)} MB`
}

export default function UsageLoading({ progress, source }: { progress: UsageState['progress']; source: string }) {
  const [mountedAt] = useState(Date.now)
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 500)
    return () => window.clearInterval(timer)
  }, [])
  const elapsed = Math.max(0, Math.floor((now - (progress?.startedAt ?? mountedAt)) / 1000))
  const phase = progress?.phase ?? 'waiting'
  const percent = phase === 'downloading' && progress?.total ? Math.min(100, Math.floor(progress.received / progress.total * 100)) : undefined
  const detail = phase === 'waiting' ? `Waiting for the ${source}…`
    : phase === 'processing' ? 'Preparing the heatmap…'
      : `Downloading history · ${bytes(progress?.received ?? 0)}${progress?.total ? ` / ${bytes(progress.total)}` : ''}`
  return (
    <LoadingIndicator label="Loading token history"
      detail={<>{detail}{percent !== undefined && ` · ${percent}%`}<span aria-live="off"> · {elapsed}s</span></>} />
  )
}
