export interface GalleryTimeWindow {
  kind: 'window'
  anchor: string
  windowSeconds: number
}

export interface GalleryTimeRange {
  kind: 'range'
  from: string
  to: string
}

export type GalleryTimeFilter = GalleryTimeWindow | GalleryTimeRange

export interface GalleryCollection {
  slug: string
  publicDir: string
  source: 'manual' | 'photos-library'
  title: string
  subtitle?: string
  hideTitleBar?: boolean
  sortMediaByDate?: boolean
  description?: string
  schemaType?: string
  ogType?: string
  timeFilter?: GalleryTimeFilter
  fromYear?: number
  fromDate?: string
  maxCount?: number
  maxPerDay?: number
  perDayBy?: 'closest' | 'appleScore'
  minAppleScore?: number
  excludeLabels?: string[]
  keepIfLabels?: string[]
  requireCamera?: boolean
  requireCaptureTz?: boolean
  notHidden?: boolean
  photosOnly?: boolean
  notScreenshot?: boolean
  controlFiles?: string[]
}

export interface GalleryCollectionDocument {
  collection: GalleryCollection
  structure: { name: string; type: string; children?: unknown[] }[]
}

// This registry contains only routes the visitor has explicitly requested.
// The build-only collection catalogue must never be imported into a client module.
const documents = new Map<string, GalleryCollectionDocument>()
const pending = new Map<string, Promise<GalleryCollectionDocument | null>>()

export function loadGalleryCollection(slug: string): Promise<GalleryCollectionDocument | null> {
  const normalized = slug.toLowerCase()
  if (!/^[a-z0-9][a-z0-9-]*$/.test(normalized)) return Promise.resolve(null)
  const cached = documents.get(normalized)
  if (cached) return Promise.resolve(cached)
  const existing = pending.get(normalized)
  if (existing) return existing
  const request = (async () => {
    try {
      const response = await fetch(`/${encodeURIComponent(normalized)}/collection.json`, {
        cache: 'no-store',
        signal: AbortSignal.timeout(15000),
      })
      if (!response.ok || !response.headers.get('content-type')?.includes('application/json')) return null
      const document = await response.json() as GalleryCollectionDocument
      const collection = document.collection
      if (!collection || collection.slug !== normalized ||
          !/^[a-z0-9][a-z0-9-]*$/.test(collection.publicDir) ||
          typeof collection.title !== 'string' || !Array.isArray(document.structure) ||
          document.structure.length !== 1 ||
          document.structure[0]?.name !== collection.publicDir ||
          document.structure[0]?.type !== 'directory') return null
      documents.set(normalized, document)
      return document
    } catch {
      return null
    } finally {
      pending.delete(normalized)
    }
  })()
  pending.set(normalized, request)
  return request
}

export function getGalleryCollectionBySlug(slug: string) {
  const normalized = slug.trim().replace(/^\/+|\/+$/g, '').toLowerCase()
  return documents.get(normalized)?.collection ?? null
}

export function getGalleryCollectionPath(collection: GalleryCollection) {
  return `/public/${collection.publicDir}`
}

export function getGalleryCollectionByPath(path: string) {
  const normalized = path.replace(/^\/public\//, '').replace(/^\/+/, '')
  return (
    [...documents.values()].map(({ collection }) => collection).find((collection) => (
      normalized === collection.publicDir ||
      normalized.startsWith(`${collection.publicDir}/`)
    )) ?? null
  )
}

export function isGalleryCollectionPath(path: string) {
  return getGalleryCollectionByPath(path) !== null
}

export function getGalleryCollectionRoutePath(path: string) {
  const collection = getGalleryCollectionByPath(path)
  return collection ? `/${collection.slug}` : null
}
