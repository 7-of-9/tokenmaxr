import { useState, useEffect, useCallback, useRef } from 'react'
import { getThumbnailPath, getVideoThumbnailPath, getVideoMicroPreviewPath, getEncodedPath } from '../utils/thumbnails'

interface ManifestItem {
  name: string
  path: string
  type: 'file' | 'directory'
  size?: number // File size in bytes (only for files)
  isImage?: boolean
  isPdf?: boolean
  isVideo?: boolean
  blobUrl?: string
  children?: ManifestItem[]
}

interface FileManifest {
  generated: string
  azureEnabled: boolean
  azureContainer: string
  structure: ManifestItem[]
}

interface AssetToLoad {
  url: string
  size: number
  type: 'image' | 'video'
  assetType: 'preview' | 'video-micro'
  path: string
  level: number
}

interface PreloadProgress {
  totalBytes: number
  loadedBytes: number
  percentage: number
  isComplete: boolean
  isPaused: boolean
}

interface PreloadControls {
  pause: () => void
  resume: () => void
  isPaused: boolean
}

const THUMBNAIL_SIZE_ESTIMATE = 50 * 1024 // 50KB estimate for JPEG thumbnails
const VIDEO_MICRO_SIZE_ESTIMATE = 300 * 1024 // 300KB estimate for MP4 video micro-previews

/**
 * Format bytes into human-readable string (B, KB, MB, GB)
 */
const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`
}

/**
 * Preload preview/thumbnail assets from the public project catalogue only.
 * Only loads thumbnails/previews (not full-size assets) for faster initial load
 * Implements a multi-level loading strategy:
 * - Level 1: Direct children of /public/projects
 * - Level 2+: Recursive subdirectories
 * 
 * Loading priority by level:
 * 1. Level 1 previews (top-level project folders)
 * 2. Level 2 previews (subdirectories)
 * 3. Level 3+ previews (recursive subdirectories)
 */
export const useAssetPreloader = (enabled: boolean = false): { progress: PreloadProgress; controls: PreloadControls } => {
  const [progress, setProgress] = useState<PreloadProgress>({
    totalBytes: 0,
    loadedBytes: 0,
    percentage: 0,
    isComplete: false,
    isPaused: false
  })
  const [isPaused, setIsPaused] = useState(false)
  const abortControllerRef = useRef<AbortController | null>(null)
  const pausedRef = useRef(false)

  const getThumbnailUrl = (originalPath: string, isVideo: boolean, blobUrl?: string): string => {
    // Use the same thumbnail path logic as the rest of the app
    // Images: directory/PREVIEW_filename.jpg
    // Videos: directory/VIDEOPREVIEW_filename.jpg
    
    if (isVideo) {
      return getVideoThumbnailPath(originalPath, blobUrl)
    } else {
      return getThumbnailPath(originalPath, blobUrl)
    }
  }

  const getPreviewFileSize = useCallback((
    items: ManifestItem[],
    previewFileName: string,
    _dirPath: string
  ): number | null => {
    // Look for the preview file in the same directory as the source file
    for (const item of items) {
      if (item.type === 'file' && item.name === previewFileName && item.size) {
        return item.size
      }
    }
    return null
  }, [])

  const collectAssetsRecursively = useCallback((
    items: ManifestItem[],
    currentLevel: number,
    currentPath: string
  ): AssetToLoad[] => {
    const assets: AssetToLoad[] = []

    for (const item of items) {
      if (item.type === 'file' && (item.isImage || item.isVideo)) {
        const isVideo = !!item.isVideo
        const itemPath = item.path.startsWith('/') ? item.path : `/${item.path}`
        
        // Skip if this file is already a preview/thumbnail file
        // These files are the output of the preview generation process, not source files
        const fileName = item.name || itemPath.split('/').pop() || ''
        const isAlreadyPreview = fileName.startsWith('PREVIEW_') || 
                                 fileName.startsWith('PDFPREVIEW_') || 
                                 fileName.startsWith('VIDEOPREVIEW_')
        
        if (isAlreadyPreview) {
          // Skip - don't create previews of preview files
          continue
        }
        
        // Determine preview file names
        const baseName = fileName.replace(/\.[^/.]+$/, '')
        const previewFileName = isVideo ? `VIDEOPREVIEW_${baseName}.jpg` : `PREVIEW_${baseName}.jpg`
        const previewFileSize = getPreviewFileSize(items, previewFileName, currentPath)
        
        // Add JPEG thumbnail (used as poster image)
        const previewUrl = getThumbnailUrl(itemPath, isVideo, item.blobUrl)
        const encodedPreviewUrl = getEncodedPath(previewUrl)
        
        assets.push({
          url: encodedPreviewUrl,
          size: previewFileSize || THUMBNAIL_SIZE_ESTIMATE, // Use actual size or fallback to estimate
          type: item.isImage ? 'image' : 'video',
          assetType: 'preview',
          path: item.path,
          level: currentLevel
        })
        
        // For videos, also add the MP4 micro-preview (the actual video that plays on tiles)
        if (isVideo) {
          const videoMicroFileName = `VIDEOPREVIEW_${baseName}.mp4`
          const videoMicroFileSize = getPreviewFileSize(items, videoMicroFileName, currentPath)
          
          const videoMicroPreviewUrl = getVideoMicroPreviewPath(itemPath, item.blobUrl)
          const encodedVideoMicroUrl = getEncodedPath(videoMicroPreviewUrl)
          
          assets.push({
            url: encodedVideoMicroUrl,
            size: videoMicroFileSize || VIDEO_MICRO_SIZE_ESTIMATE, // Use actual size or fallback to estimate
            type: 'video',
            assetType: 'video-micro',
            path: item.path,
            level: currentLevel
          })
        }
      } else if (item.type === 'directory' && item.children) {
        // Recurse into subdirectories
        const subAssets = collectAssetsRecursively(
          item.children,
          currentLevel + 1,
          item.path
        )
        assets.push(...subAssets)
      }
    }

    return assets
  }, [getPreviewFileSize])

  const loadAsset = useCallback((asset: AssetToLoad, signal?: AbortSignal): Promise<{ success: boolean; asset: AssetToLoad; error?: string }> => {
    return new Promise((resolve) => {
      const executeLoad = async () => {
      // Check if aborted before starting
      if (signal?.aborted) {
        resolve({ success: false, asset, error: 'aborted' })
        return
      }
      
      if (asset.assetType === 'video-micro') {
        // For MP4 video micro-previews, use fetch instead of video elements
        // This ensures the video is stored in the browser's HTTP cache,
        // which works much better on iOS than the media cache used by <video> tags
        // Note: Requires CORS to be enabled on the storage container
        try {
          const response = await fetch(asset.url, { 
            method: 'GET',
            mode: 'cors',
            signal // Pass abort signal to fetch
          })
          
          if (!response.ok) {
            console.error(`❌ Preload failed (fetch): ${asset.url} - ${response.status}`)
            resolve({ success: false, asset, error: `http_error_${response.status}` })
            return
          }
          
          // Consume body to ensure full download to disk cache
          await response.blob()
          resolve({ success: true, asset })
        } catch (error: any) {
          if (error.name === 'AbortError') {
            resolve({ success: false, asset, error: 'aborted' })
          } else {
            console.error(`❌ Preload failed (fetch): ${asset.url}`, error)
            resolve({ success: false, asset, error: 'network_error' })
          }
        }
      } else {
        // For JPEG thumbnails (preview), load as images
        const img = new Image()
        
        const timeout = setTimeout(() => {
          // Network timeout after 10 seconds
          console.error(`❌ Preload timeout: ${asset.url}`)
          resolve({ success: false, asset, error: 'timeout' })
        }, 10000)
        
        // Handle abort signal for images
        const handleAbort = () => {
          clearTimeout(timeout)
          img.src = '' // Cancel image load
          resolve({ success: false, asset, error: 'aborted' })
        }
        
        if (signal) {
          signal.addEventListener('abort', handleAbort)
        }
        
        img.onload = () => {
          clearTimeout(timeout)
          if (signal) signal.removeEventListener('abort', handleAbort)
          resolve({ success: true, asset })
        }
        img.onerror = (e) => {
          clearTimeout(timeout)
          if (signal) signal.removeEventListener('abort', handleAbort)
          console.error(`❌ Preload failed (image): ${asset.url}`, e)
          resolve({ success: false, asset, error: 'load_error' })
        }
        img.src = asset.url
      }
    }
    
    executeLoad()
  })
}, [])

  const preloadAssets = useCallback(async () => {
    console.log('🚀 Starting global asset preloading (low priority)...')

    // Create new AbortController for this preload session
    abortControllerRef.current = new AbortController()
    pausedRef.current = false

    try {
      // Load the file manifest
      const response = await fetch('/file-manifest.json')
      if (!response.ok) {
        throw new Error('Failed to load file manifest')
      }

      const manifest: FileManifest = await response.json()

      // Only the public project catalogue participates in global preloading.
      const projectsDir = manifest.structure.find(
        item => item.type === 'directory' && item.name === 'projects'
      )
      if (!projectsDir?.children) {
        console.error('No project directories found in manifest')
        setProgress({ totalBytes: 0, loadedBytes: 0, percentage: 100, isComplete: true, isPaused: false })
        return
      }

      // Collect preview assets from public projects.
      const allAssets: AssetToLoad[] = []

      if (projectsDir?.children) {
        const projectAssets = collectAssetsRecursively(projectsDir.children, 1, '/public/projects')
        allAssets.push(...projectAssets)
      }


      if (allAssets.length === 0) {
        console.log('No assets to preload')
        setProgress({ totalBytes: 0, loadedBytes: 0, percentage: 100, isComplete: true, isPaused: false })
        return
      }

      // Sort assets by level to load level 1 first, then level 2, etc.
      allAssets.sort((a, b) => a.level - b.level)

      // Calculate total bytes upfront from ALL assets
      const totalBytes = allAssets.reduce((sum, asset) => sum + asset.size, 0)
      const totalAssets = allAssets.length
      
      // Check if we're using actual sizes or estimates
      const usingActualSizes = allAssets.some(asset => asset.size !== THUMBNAIL_SIZE_ESTIMATE && asset.size !== VIDEO_MICRO_SIZE_ESTIMATE)
      const sizeLabel = usingActualSizes ? 'actual' : 'estimated'
      
      console.log(`📊 Preloading ${totalAssets} previews (${formatBytes(totalBytes)} ${sizeLabel})`)

      // Initialize progress with the total
      setProgress({
        totalBytes,
        loadedBytes: 0,
        percentage: 0,
        isComplete: false,
        isPaused: false
      })

      // Group assets by level for organized loading
      const assetsByLevel = new Map<number, AssetToLoad[]>()
      for (const asset of allAssets) {
        if (!assetsByLevel.has(asset.level)) {
          assetsByLevel.set(asset.level, [])
        }
        assetsByLevel.get(asset.level)!.push(asset)
      }

      const levels = Array.from(assetsByLevel.keys()).sort((a, b) => a - b)
      let totalLoadedBytes = 0 // Track cumulative loaded bytes across ALL levels
      let totalLoadedAssets = 0
      let totalSuccessful = 0
      let totalFailed = 0
      const failedAssets: AssetToLoad[] = [] // Track failed assets for summary
      const BATCH_SIZE = 12 // Increased to 12 per user request

      for (const level of levels) {
        const levelAssets = assetsByLevel.get(level)!
        const levelBytes = levelAssets.reduce((sum, asset) => sum + asset.size, 0)
        console.log(`🔄 Level ${level}: Loading ${levelAssets.length} previews (${(levelBytes / 1024 / 1024).toFixed(2)} MB)`)
        
        // Log first few video micro-previews for debugging
        const videoMicros = levelAssets.filter(a => a.assetType === 'video-micro').slice(0, 3)
        if (videoMicros.length > 0) {
          console.log(`   📹 Sample video micro-previews:`, videoMicros.map(v => v.url))
        }

        let levelSuccessful = 0
        let levelFailed = 0

        // Load in batches for this level
        for (let i = 0; i < levelAssets.length; i += BATCH_SIZE) {
          // Check if paused before each batch
          while (pausedRef.current && !abortControllerRef.current?.signal.aborted) {
            await new Promise(r => setTimeout(r, 100))
          }
          
          // Check if aborted
          if (abortControllerRef.current?.signal.aborted) {
            console.log('⏸️ Global preloading aborted')
            return
          }
          
          const batch = levelAssets.slice(i, i + BATCH_SIZE)
          
          // Load batch in parallel and track results
          // Add small delay between batches to be gentle on the network
          if (i > 0) await new Promise(r => setTimeout(r, 100))
          
          const results = await Promise.all(batch.map(asset => loadAsset(asset, abortControllerRef.current?.signal)))
          
          // Count successes and failures
          // Important: Count BOTH successes AND failures as progress
          // This ensures the progress bar always reaches 100% even if network drops
          results.forEach(result => {
            if (result.success) {
              levelSuccessful++
              totalSuccessful++
            } else {
              levelFailed++
              totalFailed++
              failedAssets.push(result.asset)
            }
          })
          
          // Update cumulative progress across all levels
          // Count ALL assets (successful + failed) toward progress
          const batchBytes = batch.reduce((sum, asset) => sum + asset.size, 0)
          totalLoadedBytes += batchBytes
          totalLoadedAssets += batch.length
          
          const percentage = Math.round((totalLoadedBytes / totalBytes) * 100)
          
          setProgress({
            totalBytes,
            loadedBytes: totalLoadedBytes,
            percentage,
            isComplete: false,
            isPaused: pausedRef.current
          })
        }
        
        console.log(`✅ Level ${level} complete: ${levelSuccessful} loaded, ${levelFailed} skipped`)
      }
      
      // Calculate actual bytes loaded (only count successful loads)
      const actualBytesLoaded = totalSuccessful * (totalBytes / totalAssets)
      
      // Final summary with error warning if needed
      if (totalFailed > 0) {
        console.warn(
          `⚠️ Preloading complete with errors: ${totalSuccessful}/${totalAssets} loaded, ${totalFailed} failed`
        )
        console.warn(`   📦 Total bytes loaded: ${formatBytes(actualBytesLoaded)} (estimated)`)
        
        // Group failed assets by type
        const failedByType = {
          'preview': failedAssets.filter(a => a.assetType === 'preview').length,
          'video-micro': failedAssets.filter(a => a.assetType === 'video-micro').length
        }
        
        console.error(`   Failed breakdown: ${failedByType.preview} thumbnails, ${failedByType['video-micro']} video previews`)
        
        // Show first few failures as examples
        const sampleFails = failedAssets.slice(0, 3)
        if (sampleFails.length > 0) {
          console.error(`   Sample failures:`)
          sampleFails.forEach(asset => {
            console.error(`      ❌ [${asset.assetType}] ${asset.path}`)
          })
        }
      } else {
        console.warn(`🎉 Preloading complete: ${totalSuccessful} previews loaded successfully`)
        console.warn(`   📦 Total bytes loaded: ${formatBytes(totalBytes)} (estimated)`)
      }

      setProgress({
        totalBytes,
        loadedBytes: totalBytes,
        percentage: 100,
        isComplete: true,
        isPaused: false
      })
    } catch (error) {
      console.error('❌ Critical error during asset preloading:', error)
      // Mark as complete even on catastrophic failure
      setProgress({ totalBytes: 0, loadedBytes: 0, percentage: 100, isComplete: true, isPaused: false })
    }
  }, [collectAssetsRecursively, loadAsset])

  const pause = useCallback(() => {
    console.log('⏸️ Pausing global preloader (high-priority asset loading)')
    pausedRef.current = true
    setIsPaused(true)
    setProgress(prev => ({ ...prev, isPaused: true }))
  }, [])

  const resume = useCallback(() => {
    console.log('▶️ Resuming global preloader')
    pausedRef.current = false
    setIsPaused(false)
    setProgress(prev => ({ ...prev, isPaused: false }))
  }, [])

  useEffect(() => {
    if (enabled) {
      preloadAssets()
    }
    
    // Cleanup: abort on unmount
    return () => {
      if (abortControllerRef.current) {
        abortControllerRef.current.abort()
      }
    }
  }, [enabled, preloadAssets])

  return {
    progress,
    controls: {
      pause,
      resume,
      isPaused
    }
  }
}
