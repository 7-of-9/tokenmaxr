import { useEffect, useRef } from 'react'
import { useBackgroundTheme } from '../contexts/BackgroundThemeContext'
import './UnifiedBackgroundControls.css'

const UnifiedBackgroundControls = () => {
  const { 
    currentTheme, 
    previousTheme, 
    nextTheme, 
    opacity, 
    grayscale, 
    setOpacity, 
    setGrayscale,
    showControls,
    toggleControls,
    backgroundEnabled,
    toggleBackground
  } = useBackgroundTheme()

  const panelRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!showControls) return

    const handleClickOutside = (event: MouseEvent) => {
      console.log('🎛️ [BackgroundControls] Click outside detected', {
        target: (event.target as HTMLElement).tagName,
        inPanel: panelRef.current?.contains(event.target as Node)
      })
      
      // If clicking toggle area (handled by its own onClick) or inside panel, ignore
      if (panelRef.current && !panelRef.current.contains(event.target as Node)) {
        // Check if target is the toggle area (which might be mounted or not depending on logic)
        // In our case, the toggle area is only mounted when !showControls.
        // So any click outside panel when showControls is true is a close action.
        toggleControls()
      }
    }

    const handleScroll = () => {
      console.log('🎛️ [BackgroundControls] Scroll detected, closing controls')
      toggleControls()
    }

    // Add listeners with delay to prevent immediate closing if triggered by opening click
    const timer = setTimeout(() => {
        document.addEventListener('mousedown', handleClickOutside)
        window.addEventListener('scroll', handleScroll, { passive: true })
    }, 100)

    return () => {
      clearTimeout(timer)
      document.removeEventListener('mousedown', handleClickOutside)
      window.removeEventListener('scroll', handleScroll)
    }
  }, [showControls, toggleControls])

  const handleOpacityChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setOpacity(parseFloat(e.target.value))
  }

  const handleGrayscaleChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    setGrayscale(parseFloat(e.target.value))
  }

  const handlePrevious = () => {
    if (!backgroundEnabled) {
      toggleBackground()
    }
    previousTheme()
  }

  const handleNext = () => {
    if (!backgroundEnabled) {
      toggleBackground()
    }
    nextTheme()
  }

  return (
    <>
          {/* Click area at top to toggle controls */}
          {!showControls && (
        <div 
          className="bc-toggle-area" 
          onClick={toggleControls}
          title="Click to toggle background controls"
        />
          )}
      
      {/* Unified controls bar */}
      <div 
        ref={panelRef}
        className={`bc-unified ${showControls ? 'bc-visible' : 'bc-hidden'}`}
        onClick={(e) => {
                  // Click on the bar itself toggles if not clicking interactive elements
                  if (e.target === e.currentTarget) {
                      toggleControls()
                  }
              }}
      >
        {/* Theme switcher */}
        <button
          className="bc-btn bc-prev"
          onClick={handlePrevious}
          aria-label="Previous background theme"
        >
          &lt;
        </button>
        
        <div className="bc-theme-name">
          {backgroundEnabled ? currentTheme.name : ''}
        </div>
        
        <button
          className="bc-btn bc-toggle-btn"
          onClick={toggleBackground}
          title={backgroundEnabled ? 'Disable animated background' : 'Enable animated background'}
          aria-label={backgroundEnabled ? 'Disable animated background' : 'Enable animated background'}
        >
          {backgroundEnabled ? '⊘' : '◉'}
        </button>

        <button
          className="bc-btn bc-next"
          onClick={handleNext}
          aria-label="Next background theme"
        >
          &gt;
        </button>
        
        {/* Divider */}
        <div className="bc-divider" />
        
        {/* Opacity control */}
        <div className={`bc-control ${!backgroundEnabled ? 'bc-disabled' : ''}`}>
          <label htmlFor="bc-opacity" className="bc-label">
            <span className="bc-icon">◐</span>
            <span className="bc-value">{Math.round(opacity * 100)}%</span>
          </label>
          <input
            id="bc-opacity"
            type="range"
            min="0"
            max="1"
            step="0.05"
            value={opacity}
            onChange={handleOpacityChange}
            className="bc-slider"
            disabled={!backgroundEnabled}
          />
        </div>
        
        {/* Monochrome control */}
        <div className={`bc-control ${!backgroundEnabled ? 'bc-disabled' : ''}`}>
          <label htmlFor="bc-grayscale" className="bc-label">
            <span className="bc-icon">◑</span>
            <span className="bc-value">{Math.round(grayscale * 100)}%</span>
          </label>
          <input
            id="bc-grayscale"
            type="range"
            min="0"
            max="1"
            step="0.05"
            value={grayscale}
            onChange={handleGrayscaleChange}
            className="bc-slider"
            disabled={!backgroundEnabled}
          />
        </div>
      </div>
    </>
  )
}

export default UnifiedBackgroundControls

