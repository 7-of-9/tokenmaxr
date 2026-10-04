// GitHub's contribution-graph colours and levels for the /tokens heatmap (SPEC "Visual design").
// Pure functions only, so heat.test.ts can run them under plain Node.

/** GitHub's dark theme as github.com draws it today (sampled from the owner's reference screenshot): an empty day,
 *  then levels 1–4. It replaced the older #161b22 / #0e4429 / #006d32 / #26a641 / #39d353 set. AgentsPage.css
 *  holds the same values as --gh-l0…--gh-l4; keep the two in step. */
export const GH_LEVELS = ['#151b23', '#033a16', '#196c2e', '#2ea043', '#56d364'] as const

export type HeatLevel = 0 | 1 | 2 | 3 | 4

/** Linear-interpolated quantile of an ascending array. */
export function quantile(sorted: number[], q: number) {
  if (sorted.length === 0) return NaN
  const pos = (sorted.length - 1) * Math.min(1, Math.max(0, q))
  const base = Math.floor(pos)
  const next = sorted[Math.min(sorted.length - 1, base + 1)]
  return sorted[base] + (next - sorted[base]) * (pos - base)
}

export interface HeatScale {
  /** Upper bounds of levels 1, 2 and 3 (the quartiles of the non-empty days); level 4 is everything above. */
  bounds: [number, number, number]
  /** 0 for an empty day, else 1–4. */
  level: (value: number) => HeatLevel
}

/**
 * GitHub's rule: the non-empty days are split at their quartiles, so each green holds about a quarter of
 * the active days whatever the spread (one extreme day can never flatten the rest). Days with nothing are 0.
 */
export function makeHeatScale(values: number[]): HeatScale {
  const sorted = values.filter((v) => v > 0 && Number.isFinite(v)).sort((a, b) => a - b)
  if (sorted.length === 0) return { bounds: [0, 0, 0], level: () => 0 }
  const bounds: [number, number, number] = [quantile(sorted, 0.25), quantile(sorted, 0.5), quantile(sorted, 0.75)]
  return {
    bounds,
    level: (value) => {
      if (!(value > 0)) return 0
      if (value <= bounds[0]) return 1
      if (value <= bounds[1]) return 2
      if (value <= bounds[2]) return 3
      return 4
    },
  }
}

/** Relative luminance of #rrggbb (WCAG). */
export function luminance(hex: string) {
  const channel = (i: number) => {
    const v = parseInt(hex.slice(1 + i * 2, 3 + i * 2), 16) / 255
    return v <= 0.04045 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4
  }
  return 0.2126 * channel(0) + 0.7152 * channel(1) + 0.0722 * channel(2)
}

export function contrastRatio(a: string, b: string) {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x + 0.05) / (y + 0.05)
}
