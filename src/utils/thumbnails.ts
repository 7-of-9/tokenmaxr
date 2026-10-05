/**
 * Utility functions for handling thumbnail paths
 */

// Base URL for CDN assets
const CDN_BASE_URL = 'https://cdn.d0m1.com/d0m1-media';

/**
 * Helper to construct a full CDN URL from a local path if needed
 * @param path - Local path (e.g. "/projects/foo.jpg")
 * @returns Full CDN URL (e.g. "https://cdn.d0m1.com/d0m1-media/projects/foo.jpg")
 */
const toPublicPath = (path: string): string => {
  let cleanPath = path;
  if (cleanPath.startsWith('/public')) cleanPath = cleanPath.substring(7);
  if (!cleanPath.startsWith('/')) cleanPath = `/${cleanPath}`;
  return cleanPath;
};

const toCdnUrl = (path: string): string => {
  if (!path) return path;
  if (path.startsWith('http://') || path.startsWith('https://')) return path;
  return `${CDN_BASE_URL}${toPublicPath(path)}`;
};

// Match the validated media publication revision, including existing CDN URLs.
const bustCachedCdn404 = (url: string): string => {
  if (!url) return url
  if (!url.includes('cdn.d0m1.com')) return url
  const parsed = new URL(url)
  parsed.searchParams.set('v', 'privacy-20261003')
  return parsed.toString()
}

/**
 * Prefer an Azure blob URL. In local dev, fall back to the Vite public path
 * so newly collected galleries work before they are synced to the CDN.
 */
const toAssetUrl = (path: string, blobUrl?: string): string => {
  if (import.meta.env.DEV) return toPublicPath(path);
  if (blobUrl) return blobUrl;
  return toCdnUrl(path);
};

/**
 * Converts an image path to its corresponding thumbnail path
 * @param imagePath - Original image path (e.g., "/images/photo.jpg")
 * @param blobUrl - Optional CDN URL for the original file
 * @returns Thumbnail path (e.g., "/images/PREVIEW_photo.jpg" or CDN equivalent)
 */
export const getThumbnailPath = (imagePath: string, blobUrl?: string): string => {
  if (!imagePath) return imagePath;
  
  // Use blobUrl if available, otherwise construct CDN URL from path
  // We want ALL assets to load from CDN in all environments
  const sourcePath = toAssetUrl(imagePath, blobUrl);
  
  // Extract directory and filename
  const lastSlashIndex = sourcePath.lastIndexOf('/');
  const directory = sourcePath.substring(0, lastSlashIndex + 1);
  const filename = sourcePath.substring(lastSlashIndex + 1);
  
  // If already a preview file, return as-is
  if (filename.startsWith('PREVIEW_') || filename.startsWith('PDFPREVIEW_') || filename.startsWith('VIDEOPREVIEW_')) {
    return bustCachedCdn404(sourcePath);
  }
  
  // Remove file extension to get base name
  const lastDotIndex = filename.lastIndexOf('.');
  const baseName = lastDotIndex > 0 ? filename.substring(0, lastDotIndex) : filename;
  
  // Create thumbnail path with PREVIEW_ prefix and .jpg extension
  return bustCachedCdn404(`${directory}PREVIEW_${baseName}.jpg`);
};

/**
 * Checks if an image should use a thumbnail (not SVG files)
 * @param imagePath - Image path to check
 * @returns True if thumbnail should be used, false otherwise
 */
export const shouldUseThumbnail = (imagePath: string): boolean => {
  return !imagePath.toLowerCase().endsWith('.svg');
};

/**
 * Checks if a file is a PDF
 * @param filePath - File path to check
 * @returns True if it's a PDF file
 */
export const isPdfFile = (filePath: string): boolean => {
  return filePath.toLowerCase().endsWith('.pdf');
};

/**
 * Gets the PDF thumbnail path
 * @param pdfPath - Original PDF path
 * @param blobUrl - Optional CDN URL for the original file
 * @returns PDF thumbnail path with PDFPREVIEW_ prefix
 */
export const getPdfThumbnailPath = (pdfPath: string, blobUrl?: string): string => {
  if (!pdfPath) return pdfPath;
  
  const sourcePath = toAssetUrl(pdfPath, blobUrl);
  
  // Extract directory and filename
  const lastSlashIndex = sourcePath.lastIndexOf('/');
  const directory = sourcePath.substring(0, lastSlashIndex + 1);
  const filename = sourcePath.substring(lastSlashIndex + 1);
  
  // If already a preview file, return as-is
  if (filename.startsWith('PDFPREVIEW_') || filename.startsWith('PREVIEW_') || filename.startsWith('VIDEOPREVIEW_')) {
    return bustCachedCdn404(sourcePath);
  }
  
  // Remove file extension to get base name
  const lastDotIndex = filename.lastIndexOf('.');
  const baseName = lastDotIndex > 0 ? filename.substring(0, lastDotIndex) : filename;
  
  // Create PDF thumbnail path with PDFPREVIEW_ prefix and .jpg extension
  return bustCachedCdn404(`${directory}PDFPREVIEW_${baseName}.jpg`);
};

/**
 * Checks if a file is a video
 * @param filePath - File path to check
 * @returns True if it's a video file
 */
export const isVideoFile = (filePath: string): boolean => {
  const videoExtensions = ['.mp4', '.webm', '.mov', '.avi', '.mkv', '.flv', '.wmv', '.m4v'];
  return videoExtensions.some(ext => filePath.toLowerCase().endsWith(ext));
};

/**
 * Gets the video thumbnail path
 * @param videoPath - Original video path
 * @param blobUrl - Optional CDN URL for the original file
 * @returns Video thumbnail path with VIDEOPREVIEW_ prefix
 */
export const getVideoThumbnailPath = (videoPath: string, blobUrl?: string): string => {
  if (!videoPath) return videoPath;

  const sourcePath = toAssetUrl(videoPath, blobUrl);

  // Extract directory and filename
  const lastSlashIndex = sourcePath.lastIndexOf('/');
  const directory = sourcePath.substring(0, lastSlashIndex + 1);
  const filename = sourcePath.substring(lastSlashIndex + 1);

  // If already a preview file, return as-is
  if (filename.startsWith('VIDEOPREVIEW_') || filename.startsWith('PREVIEW_') || filename.startsWith('PDFPREVIEW_')) {
    return bustCachedCdn404(sourcePath);
  }

  // Remove file extension to get base name
  const lastDotIndex = filename.lastIndexOf('.');
  const baseName = lastDotIndex > 0 ? filename.substring(0, lastDotIndex) : filename;

  // Create video thumbnail path with VIDEOPREVIEW_ prefix and .jpg extension
  return bustCachedCdn404(`${directory}VIDEOPREVIEW_${baseName}.jpg`);
};

/**
 * Gets the video micro-preview path (for directory tiles)
 * @param videoPath - Original video path
 * @param blobUrl - Optional CDN URL for the original file
 * @returns Video micro-preview path with VIDEOPREVIEW_ prefix and .mp4 extension
 */
export const getVideoMicroPreviewPath = (videoPath: string, blobUrl?: string): string => {
  if (!videoPath) return videoPath;

  const sourcePath = toAssetUrl(videoPath, blobUrl);

  // Extract directory and filename
  const lastSlashIndex = sourcePath.lastIndexOf('/');
  const directory = sourcePath.substring(0, lastSlashIndex + 1);
  const filename = sourcePath.substring(lastSlashIndex + 1);

  // If already a video preview file, return as-is
  if (filename.startsWith('VIDEOPREVIEW_')) {
    return bustCachedCdn404(sourcePath);
  }

  // Remove file extension to get base name
  const lastDotIndex = filename.lastIndexOf('.');
  const baseName = lastDotIndex > 0 ? filename.substring(0, lastDotIndex) : filename;

  // Create video thumbnail path with VIDEOPREVIEW_ prefix and .mp4 extension
  return bustCachedCdn404(`${directory}VIDEOPREVIEW_${baseName}.mp4`);
};

/**
 * Encodes a path for use as a URL, handling special characters like spaces and parentheses
 * @param path - File path
 * @returns Encoded URL string
 */
export const getEncodedPath = (path: string): string => {
  if (!path) return path;
  const needsEncoding = path.includes(' ') || path.includes('(') || path.includes(')');
  return needsEncoding ? encodeURI(path) : path;
};



/**
 * Gets the appropriate image path - thumbnail for display, original for full-screen
 * @param originalPath - Original image path
 * @param useOriginal - Whether to use original (for full-screen) or thumbnail
 * @param blobUrl - Optional Azure blob URL for the original file
 * @returns Appropriate image path
 */
export const getImagePath = (originalPath: string, useOriginal: boolean = false, blobUrl?: string): string => {
  if (useOriginal || !shouldUseThumbnail(originalPath)) {
    // If requesting original and we have a blob URL, use it; otherwise construct CDN path
    return bustCachedCdn404(toAssetUrl(originalPath, blobUrl));
  }
  return getThumbnailPath(originalPath, blobUrl);
};

/**
 * Gets the appropriate video path - uses Azure blob URL if available for better mobile compatibility
 * @param originalPath - Original video path
 * @param blobUrl - Optional Azure blob URL for the video file
 * @returns Appropriate video path (prefers blob URL for better codec support)
 */
export const getVideoPath = (originalPath: string, blobUrl?: string): string => {
  // Prefer blob URL if available, otherwise construct CDN path
  return bustCachedCdn404(toAssetUrl(originalPath, blobUrl));
};
