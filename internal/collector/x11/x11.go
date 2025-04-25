package x11

import (
	"context"
	"fmt"
	"log"
	"quantme/internal/event"
	"strings"
	"time"

	"github.com/BurntSushi/xgbutil"
	"github.com/BurntSushi/xgbutil/ewmh"
	"github.com/BurntSushi/xgbutil/icccm"
)

type X11Collector struct {
	X            *xgbutil.XUtil
	lastFocus    event.FocusInfo
	stopChan     chan struct{}
	focusRequest chan chan event.FocusInfo // Channel to request current focus
}

func NewX11Collector() (*X11Collector, error) {
	X, err := xgbutil.NewConn()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to X server: %w", err)
	}

	// Check if EWMH is supported (needed for _NET_ACTIVE_WINDOW, _NET_WM_NAME)
	if _, err := ewmh.CurrentDesktopGet(X); err != nil {
		log.Printf("Warning: EWMH potentially not supported by Window Manager: %v", err)
		// Could fallback to older methods if needed, but modern desktops support EWMH
	}

	return &X11Collector{
		X:            X,
		stopChan:     make(chan struct{}),
		focusRequest: make(chan chan event.FocusInfo),
	}, nil
}

func (c *X11Collector) getActiveWindowInfo() (event.FocusInfo, error) {
	activeWinID, err := ewmh.ActiveWindowGet(c.X)
	if err != nil {
		// Don't log every time, could be window manager switching desktops etc.
		// log.Printf("Could not get active window ID: %v", err)
		return event.FocusInfo{}, fmt.Errorf("could not get active window ID: %w", err)
	}

	if activeWinID == 0 {
		return event.FocusInfo{AppName: "None", Title: "No Active Window"}, nil // No window focused
	}

	// Get window title (_NET_WM_NAME preferred, fallback to WM_NAME)
	title, err := ewmh.WmNameGet(c.X, activeWinID)
	if err != nil || title == "" {
		// Fallback to WM_NAME (ICCCM)
		title, err = icccm.WmNameGet(c.X, activeWinID)
		if err != nil || title == "" {
			// log.Printf("Could not get window title for ID %d: %v", activeWinID, err)
			title = "Unknown Title"
		}
	}

	// Get application name (WM_CLASS)
	appName := "Unknown App"
	classHints, err := icccm.WmClassGet(c.X, activeWinID)
	if err == nil && classHints != nil {
		// Often, the Class is the application name
		appName = classHints.Class
	} else {
		// log.Printf("Could not get WM_CLASS for ID %d: %v", activeWinID, err)
		// You could try getting _NET_WM_PID and looking up the process name, but that's more involved
	}

	return event.FocusInfo{AppName: appName, Title: title}, nil
}

func (c *X11Collector) Start(ctx context.Context, interval time.Duration, output chan<- event.Event, focusOnly bool) error {
	log.Printf("Starting X11 collector (interval: %s, focusOnly: %t)", interval, focusOnly)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Get initial focus to establish baseline
	// Sometimes immediately after start, WM might not report correctly, try a few times
	var initialFocus event.FocusInfo
	var err error
	for i := 0; i < 3; i++ {
		initialFocus, err = c.getActiveWindowInfo()
		if err == nil {
			c.lastFocus = initialFocus
			// Optionally send an initial focus event?
			// output <- event.Event{ ... initial focus ... }
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		log.Printf("Warning: Failed to get initial window focus: %v", err)
		// Continue anyway, maybe it recovers
	}

	for {
		select {
		case <-ctx.Done():
			log.Println("X11 collector stopping due to context cancellation.")
			return ctx.Err()
		case <-c.stopChan:
			log.Println("X11 collector stopping.")
			return nil
		case respChan := <-c.focusRequest:
			// Handle synchronous request for current focus
			currentFocus, err := c.getActiveWindowInfo()
			if err != nil {
				log.Printf("Error getting current focus on request: %v", err)
				// Optionally send back an empty struct or error indication
			}
			respChan <- currentFocus // Send back result (even if empty/error)
		case <-ticker.C:
			// In focusOnly mode, we might check Pomodoro state here (passed via context or another channel)
			// For simplicity now, we assume the app layer handles filtering based on collect_mode

			currentFocus, err := c.getActiveWindowInfo()
			if err != nil {
				// Don't spam logs if window is temporarily unavailable
				// log.Printf("Error getting active window: %v", err)
				continue
			}

			// Normalize empty strings for comparison
			if c.lastFocus.AppName == "" {
				c.lastFocus.AppName = "Unknown App"
			}
			if c.lastFocus.Title == "" {
				c.lastFocus.Title = "Unknown Title"
			}

			if currentFocus.AppName != c.lastFocus.AppName || currentFocus.Title != c.lastFocus.Title {
				log.Printf("Focus Changed: App='%s', Title='%s'", currentFocus.AppName, currentFocus.Title)
				changeEvent := event.Event{
					Timestamp:   time.Now(),
					Type:        event.EventTypeFocusChange,
					AppName:     currentFocus.AppName,
					WindowTitle: currentFocus.Title,
					Tag:         c.lastFocus.AppName, // Previous app as tag? Or maybe Pomodoro state?
					Notes:       fmt.Sprintf("Previous: %s - %s", c.lastFocus.AppName, Truncate(c.lastFocus.Title, 50)),
				}
				select {
				case output <- changeEvent:
					c.lastFocus = currentFocus
				case <-ctx.Done():
					return ctx.Err()
				case <-c.stopChan:
					return nil
				}
			}
		}
	}
}

func (c *X11Collector) GetCurrentFocus() (event.FocusInfo, error) {
	respChan := make(chan event.FocusInfo)
	select {
	case c.focusRequest <- respChan:
		select {
		case focus := <-respChan:
			return focus, nil
		case <-time.After(1 * time.Second): // Timeout
			return event.FocusInfo{}, fmt.Errorf("timeout waiting for current focus response")
		}
	case <-time.After(100 * time.Millisecond): // Timeout if collector loop is blocked
		// Fallback to direct call if request channel is busy? Risky if X connection is bad.
		// return c.getActiveWindowInfo()
		return event.FocusInfo{}, fmt.Errorf("timeout sending focus request to collector")
	}
}

func (c *X11Collector) Stop() error {
	log.Println("Sending stop signal to X11 collector.")
	close(c.stopChan)
	// X connection is likely managed by xgbutil, closing might happen automatically
	// or we might need c.X.Conn().Close() if we manage it explicitly.
	// For now, rely on process exit to close the connection.
	return nil
}

func Truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	// Try to truncate at a space if possible near the end
	if idx := strings.LastIndex(s[:maxLen-3], " "); idx > maxLen/2 {
		return s[:idx] + "..."
	}
	return s[:maxLen-3] + "..."
}
