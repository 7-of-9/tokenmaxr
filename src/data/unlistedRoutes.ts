// d0m1.com's unlisted page lookup; tokenmaxr has no unlisted pages (the export replaces d0m1's own).
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

export function subscribePageRoutes(_listener: () => void) {
  return () => {}
}

export function resolveCvRoute(_pathname: string): CvRouteEntry | null {
  return null
}

export const isCvRoutePath = (_pathname: string) => false

export function loadPageRoute(_pathname: string): Promise<CvRouteEntry | null> {
  return Promise.resolve(null)
}
