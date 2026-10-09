/// <reference types="vite/client" />

declare const __BUILD_TIME__: string
declare const __AGENTS_API_ORIGIN__: string
declare const __CV_DISCOVERABLE__: boolean

// Google Analytics gtag.js
interface Window {
  gtag?: (
    command: 'event',
    action: string,
    params?: {
      description?: string
      fatal?: boolean
      [key: string]: any
    }
  ) => void
}
