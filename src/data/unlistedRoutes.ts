import vault from './unlistedRoutes.generated.json'
import { normalizeRoutePath, openRouteRecord, routeRecordId } from '../utils/pathCipher.js'

export interface CvRouteEntry {
  routePath: string
  canonicalPath: string
  filePrefix: string
  pdfPath: string | null
  locked?: boolean
  downloadName: string | null
  documentLabel: string
  title: string
  description: string
  schemaType?: string
  ogType?: string
}

// The CV routes, sealed per path by scripts/generate-cv-routes.js: a route resolves only
// when the visitor has typed it, and the bundle never lists one.
const records = (vault as { records: Record<string, string> }).records
const resolved = new Map<string, CvRouteEntry | null>()

export function resolveCvRoute(pathname: string): CvRouteEntry | null {
  const path = normalizeRoutePath(pathname)
  if (!resolved.has(path)) {
    const sealed = records[routeRecordId(path)]
    resolved.set(path, sealed ? openRouteRecord<CvRouteEntry>(path, sealed) : null)
  }
  return resolved.get(path) ?? null
}

export const isCvRoutePath = (pathname: string) => resolveCvRoute(pathname) !== null
