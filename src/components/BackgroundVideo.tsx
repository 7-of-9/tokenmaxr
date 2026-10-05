import { useEffect, useRef } from 'react'
import { useBackgroundTheme } from '../contexts/BackgroundThemeContext'
import './BackgroundVideo.css'

interface BackgroundVideoProps {
  enabled: boolean
}

const BackgroundVideo = ({ enabled }: BackgroundVideoProps) => {
  const { currentTheme, opacity, grayscale } = useBackgroundTheme()
  const videoRef = useRef<HTMLVideoElement>(null)

  // Get the appropriate video path - using high-quality resolution from Azure CDN
  const getVideoPath = () => {
    const resolution = currentTheme.resolutions.desktop
    // Use Azure CDN for background videos (not copied to dist/)
    const basePath = `https://cdn.d0m1.com/d0m1-media/scp_backg/${currentTheme.folder}/${resolution}`
    
    // Try mp4 format first (most compatible)
    const path = `${basePath}.mp4`
    console.log('📹 Video path:', path)
    return path
  }
  
  // Debug: Log when video element is mounted
  useEffect(() => {
    console.log('🎬 BackgroundVideo mounted, enabled:', enabled)
    if (videoRef.current) {
      console.log('🎬 Video element ready:', {
        src: videoRef.current.src,
        readyState: videoRef.current.readyState,
        paused: videoRef.current.paused
      })
    }
  }, [])

  // Handle theme changes - try to play, but don't force reload (let src prop handle it)
  useEffect(() => {
    if (videoRef.current) {
      console.log('🎬 Theme changed:', currentTheme.name)
      
      // iOS/Mobile requires user interaction for autoplay
      const playPromise = videoRef.current.play()
      if (playPromise !== undefined) {
        playPromise
          .then(() => {
            console.log('✅ Video autoplay started successfully')
          })
          .catch(err => {
            console.warn('⚠️ Autoplay prevented (will retry on interaction):', err)
            console.log('💡 Tip: Tap anywhere on the page to start the background video')
          })
      }
    }
  }, [currentTheme])

  // iOS/Mobile autoplay workaround - attempt to play on ANY user interaction
  useEffect(() => {
    // Include touchstart for mobile - it fires before click and is more reliable
    const events = ['touchstart', 'click', 'scroll', 'keydown', 'pointerdown']
    let hasPlayed = false

    const attemptPlay = (e: Event) => {
      // Debug: Log every interaction attempt
      console.log(`🎬 User interaction detected: ${e.type}, video state:`, {
        videoExists: !!videoRef.current,
        paused: videoRef.current?.paused,
        readyState: videoRef.current?.readyState,
        networkState: videoRef.current?.networkState,
        hasPlayed
      })
      
      if (videoRef.current && !hasPlayed) {
        // Check if video needs to play (paused OR not started yet)
        const needsPlay = videoRef.current.paused || videoRef.current.currentTime === 0
        
        if (needsPlay) {
          console.log('🎬 Attempting video play on user interaction...')
          const playPromise = videoRef.current.play()
          
          if (playPromise !== undefined) {
            playPromise
              .then(() => {
                console.log('✅ Video playing successfully!')
                hasPlayed = true
                // Remove all listeners once playing
                events.forEach(event => {
                  document.removeEventListener(event, attemptPlay, { capture: true })
                })
              })
              .catch(err => {
                console.warn('⚠️ Play attempt failed, will retry:', err.message || err)
                // Keep listeners active to try again
              })
          }
        } else {
          console.log('✅ Video already playing!')
          hasPlayed = true
          // Remove listeners
          events.forEach(event => {
            document.removeEventListener(event, attemptPlay, { capture: true })
          })
        }
      }
    }

    // Listen for various user interaction events
    // IMPORTANT: Use capture:true to catch events BEFORE other handlers
    events.forEach(event => {
      // Use passive:true for all events to avoid blocking (we don't call preventDefault)
      document.addEventListener(event, attemptPlay, { capture: true, passive: true })
    })

    // Also try to play immediately (works if user has interacted before)
    const initialTimer = setTimeout(() => attemptPlay(new Event('initial')), 200)
    
    console.log('🎬 Interaction listeners attached:', events.join(', '))

    // Cleanup
    return () => {
      clearTimeout(initialTimer)
      events.forEach(event => {
        document.removeEventListener(event, attemptPlay, { capture: true })
      })
    }
  }, [currentTheme])

  // Handle play/pause when enabled state changes
  useEffect(() => {
    console.log('🎬 Background enabled state changed:', enabled)
    if (videoRef.current) {
      if (enabled) {
        console.log('🎬 Attempting to play video (enabled=true)...')
        const playPromise = videoRef.current.play()
        if (playPromise !== undefined) {
          playPromise
            .then(() => {
              console.log('✅ Video play succeeded on enabled change')
            })
            .catch(err => {
              console.warn('⚠️ Play failed:', err)
            })
        }
      } else {
        console.log('⏸️ Pausing video (enabled=false)')
        videoRef.current.pause()
      }
    } else {
      console.warn('⚠️ Video ref not available')
    }
  }, [enabled])

  return (
    <div 
      className="background-video-container"
      style={{ 
        display: enabled ? 'block' : 'none' 
      }}
    >
      <video
        ref={videoRef}
        className="background-video"
        autoPlay
        loop
        muted
        playsInline
        preload="auto"
        src={getVideoPath()}
        style={{
          opacity: opacity,
          filter: `brightness(0.8) grayscale(${grayscale})`
        }}
      />
    </div>
  )
}

export default BackgroundVideo

