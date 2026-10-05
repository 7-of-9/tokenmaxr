import React, { createContext, useContext, useState, useEffect, useCallback } from 'react'
import type { ReactNode } from 'react'
import { useAssetPreloader } from '../hooks/useAssetPreloader'

interface PreloadProgress {
  totalBytes: number
  loadedBytes: number
  percentage: number
  isComplete: boolean
  isPaused: boolean
}

interface BackgroundTheme {
  name: string
  folder: string
  resolutions: {
    mobile: string     // fa240 or similar
    desktop: string    // hq576 or higher
  }
}

interface ThemeSettings {
  opacity: number
  grayscale: number
}

interface BackgroundThemeContextType {
  currentTheme: BackgroundTheme
  currentThemeIndex: number
  nextTheme: () => void
  previousTheme: () => void
  opacity: number
  grayscale: number
  setOpacity: (value: number) => void
  setGrayscale: (value: number) => void
  showControls: boolean
  toggleControls: () => void
    backgroundEnabled: boolean
    toggleBackground: () => void
    modalOpen: boolean
    setModalOpen: (open: boolean) => void
    preloadProgress: PreloadProgress
    preloadEnabled: boolean
    startPreloading: () => void
    pausePreloading: () => void
    resumePreloading: () => void
}

const BackgroundThemeContext = createContext<BackgroundThemeContextType | undefined>(undefined)

// Background themes configuration - Only 3 active themes
const BACKGROUND_THEMES: BackgroundTheme[] = [
  { name: 'Cieling Flame', folder: 'Cieling Flame', resolutions: { mobile: 'Cieling Flames 4K Motion Background Loop (1)-5-fa240', desktop: 'Cieling Flames 4K Motion Background Loop (1)-4-hq576' } },
  { name: 'Kaleida', folder: '0 Kaleida', resolutions: { mobile: 'Kaleida Blue 4K Motion Background Loop-18-fa240', desktop: 'Kaleida Blue 4K Motion Background Loop-12-hq576' } },
  { name: 'HD79', folder: 'HD79', resolutions: { mobile: 'Hd0079-36-fa240', desktop: 'Hd0079-23-hq576' } },
]

// Default settings for themes
const DEFAULT_SETTINGS: ThemeSettings = {
    opacity: 0.60,
    grayscale: 0
}

// LocalStorage keys
const STORAGE_KEY = 'backgroundThemeSettings'
const BACKGROUND_ENABLED_KEY = 'backgroundEnabled'

// Load settings from localStorage
const loadThemeSettings = (themeName: string): ThemeSettings => {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    if (stored) {
      const allSettings = JSON.parse(stored)
      if (allSettings[themeName]) {
        return allSettings[themeName]
      }
    }
  } catch (e) {
    console.warn('Failed to load theme settings from localStorage:', e)
  }

    // Specific defaults for Kaleida
    if (themeName === 'Kaleida') {
        return { opacity: 0.40, grayscale: 0.20 }
    }

    // Specific defaults for Cieling Flame
    if (themeName === 'Cieling Flame') {
        return { opacity: 1.00, grayscale: 0.20 }
    }

    // Specific defaults for HD79
    if (themeName === 'HD79') {
        return { opacity: 0.40, grayscale: 0.65 }
    }

  return { ...DEFAULT_SETTINGS }
}

// Save settings to localStorage
const saveThemeSettings = (themeName: string, settings: ThemeSettings) => {
  try {
    const stored = localStorage.getItem(STORAGE_KEY)
    const allSettings = stored ? JSON.parse(stored) : {}
    allSettings[themeName] = settings
    localStorage.setItem(STORAGE_KEY, JSON.stringify(allSettings))
  } catch (e) {
    console.warn('Failed to save theme settings to localStorage:', e)
  }
}

export const BackgroundThemeProvider: React.FC<{ children: ReactNode }> = ({ children }) => {
  // Set default theme to HD79 (now index 2 in the filtered list)
  const [currentThemeIndex, setCurrentThemeIndex] = useState(2)
  // Hide BC by default - user can toggle with top click
  const [showControls, setShowControls] = useState(false)
    // Background enabled/disabled state - always ON, override any persisted value
    const [backgroundEnabled, setBackgroundEnabled] = useState(true)
    // Modal open state (for hiding UI elements when modals are shown)
    const [modalOpen, setModalOpen] = useState(false)
    // Asset preloading state
    const [preloadEnabled, setPreloadEnabled] = useState(false)
  
  const currentTheme = BACKGROUND_THEMES[currentThemeIndex]
  
  // Load initial settings for the default theme
  const initialSettings = loadThemeSettings(currentTheme.name)
  const [opacity, setOpacityState] = useState(initialSettings.opacity)
  const [grayscale, setGrayscaleState] = useState(initialSettings.grayscale)
  
  // Global asset preloader - runs once and persists across pages
  const { progress: preloadProgress, controls: preloadControls } = useAssetPreloader(preloadEnabled)
  
  const startPreloading = useCallback(() => {
    setPreloadEnabled(prev => {
      if (!prev) {
        console.log('🚀 Starting global asset preloading...')
        return true
      }
      return prev
    })
  }, [])
  
  const pausePreloading = useCallback(() => {
    preloadControls.pause()
  }, [preloadControls])
  
  const resumePreloading = useCallback(() => {
    preloadControls.resume()
  }, [preloadControls])
  
  // Force background to be enabled on mount and clear localStorage
  useEffect(() => {
    try {
      localStorage.setItem(BACKGROUND_ENABLED_KEY, 'true')
      console.log('🎬 Background forced to ON')
    } catch (e) {
      console.warn('Failed to set background enabled state:', e)
    }
  }, [])

  const toggleControls = () => {
    setShowControls(prev => !prev)
  }

    const toggleBackground = () => {
        setBackgroundEnabled(prev => {
            const newValue = !prev
            try {
                localStorage.setItem(BACKGROUND_ENABLED_KEY, newValue.toString())
            } catch (e) {
                console.warn('Failed to save background enabled state:', e)
            }
            return newValue
        })
    }

  // Update settings when theme changes
  useEffect(() => {
    const settings = loadThemeSettings(currentTheme.name)
    setOpacityState(settings.opacity)
    setGrayscaleState(settings.grayscale)
  }, [currentTheme.name])

  // Save settings when they change
  const setOpacity = (value: number) => {
    setOpacityState(value)
    saveThemeSettings(currentTheme.name, { opacity: value, grayscale })
  }

  const setGrayscale = (value: number) => {
    setGrayscaleState(value)
    saveThemeSettings(currentTheme.name, { opacity, grayscale: value })
  }

  const nextTheme = () => {
    const newIndex = (currentThemeIndex + 1) % BACKGROUND_THEMES.length
    setCurrentThemeIndex(newIndex)
  }

  const previousTheme = () => {
    const newIndex = currentThemeIndex === 0 ? BACKGROUND_THEMES.length - 1 : currentThemeIndex - 1
    setCurrentThemeIndex(newIndex)
  }

  return (
    <BackgroundThemeContext.Provider
      value={{
        currentTheme,
        currentThemeIndex,
        nextTheme,
        previousTheme,
        opacity,
        grayscale,
        setOpacity,
        setGrayscale,
        showControls,
        toggleControls,
              backgroundEnabled,
              toggleBackground,
              modalOpen,
              setModalOpen,
              preloadProgress,
              preloadEnabled,
              startPreloading,
              pausePreloading,
              resumePreloading,
      }}
    >
      {children}
    </BackgroundThemeContext.Provider>
  )
}

export const useBackgroundTheme = () => {
  const context = useContext(BackgroundThemeContext)
  if (!context) {
    throw new Error('useBackgroundTheme must be used within BackgroundThemeProvider')
  }
  return context
}

