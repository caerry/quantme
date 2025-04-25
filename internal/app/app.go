package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"quantme/internal/collector/hooks"
	"quantme/internal/collector/x11"
	"quantme/internal/config"
	"quantme/internal/event"
	"quantme/internal/ipc"
	"quantme/internal/storage"

	sqlitestore "quantme/internal/storage/sqlite"
)

type App struct {
	cfg     *config.Config
	storage storage.Storage
	x11Col  *x11.X11Collector
	hookMan *hooks.HookManager
	// --- Socket Handling ---
	socketPath string
	listener   *net.UnixListener

	// Communication channels
	eventChan      chan event.Event
	hookUpdateChan chan interface{}

	wg     sync.WaitGroup
	ctx    context.Context
	cancel context.CancelFunc

	// Keep track of current Pomo state for status command
	currentPomoStatus event.PomodoroUpdate
	statusMutex       sync.RWMutex
}

func NewApp(cfg *config.Config) (*App, error) {
	ctx, cancel := context.WithCancel(context.Background())

	a := &App{
		cfg:            cfg,
		eventChan:      make(chan event.Event, 100),
		hookUpdateChan: make(chan interface{}, 50),
		socketPath:     ipc.SocketPath, // Use constant
		ctx:            ctx,
		cancel:         cancel,
	}

	// Initialize Storage
	a.storage = sqlitestore.NewSQLiteStore(cfg.DatabasePath)
	if err := a.storage.Init(ctx); err != nil {
		cancel()
		return nil, fmt.Errorf("failed to initialize storage: %w", err)
	}

	// Initialize X11 Collector
	var x11Err error
	a.x11Col, x11Err = x11.NewX11Collector()
	if x11Err != nil {
		log.Printf("Warning: Failed to initialize X11 collector: %v. Focus tracking disabled.", x11Err)
		a.x11Col = nil
	}

	// Initialize Hook Manager - Pass the app reference for event handling
	a.hookMan = hooks.NewHookManager(cfg, a.hookUpdateChan)

	return a, nil
}

// setupSocket checks for existing socket and creates the listener
func (a *App) setupSocket() error {
	// Check if socket file exists and try connecting
	if _, err := os.Stat(a.socketPath); err == nil {
		// Socket file exists, try to connect
		conn, err := net.DialTimeout("unix", a.socketPath, 1*time.Second)
		if err == nil {
			// Connection successful - another instance is likely running
			conn.Close()
			return fmt.Errorf("socket %s already active, another instance might be running", a.socketPath)
		}
		// Connection failed - socket file is stale, remove it
		log.Printf("Stale socket file found at %s, removing.", a.socketPath)
		if err := os.Remove(a.socketPath); err != nil {
			return fmt.Errorf("failed to remove stale socket file %s: %w", a.socketPath, err)
		}
	} else if !os.IsNotExist(err) {
		// Other error stating the file (permission denied?)
		return fmt.Errorf("error checking socket file %s: %w", a.socketPath, err)
	}

	// Resolve the address
	addr, err := net.ResolveUnixAddr("unix", a.socketPath)
	if err != nil {
		return fmt.Errorf("failed to resolve unix addr %s: %w", a.socketPath, err)
	}

	// Listen on the socket
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on socket %s: %w", a.socketPath, err)
	}

	// Set permissions (optional, default is usually fine based on umask)
	// if err := os.Chmod(a.socketPath, 0600); err != nil { // Example: User only R/W
	// 	listener.Close()
	// 	return fmt.Errorf("failed to set permissions on socket %s: %w", a.socketPath, err)
	// }

	a.listener = listener
	log.Printf("Listening for commands on %s", a.socketPath)
	return nil
}

// listenForCommands accepts connections and handles them
func (a *App) listenForCommands() {
	defer a.wg.Done()
	defer log.Println("Socket command listener stopped.")

	if a.listener == nil {
		log.Println("Error: Socket listener not initialized.")
		return
	}

	for {
		conn, err := a.listener.AcceptUnix()
		if err != nil {
			// Check if the error is due to the listener being closed
			select {
			case <-a.ctx.Done():
				log.Println("Listener closing due to context cancellation.")
				return // Expected error on shutdown
			default:
				log.Printf("Failed to accept connection: %v", err)
				// Avoid tight loop on persistent error
				if ne, ok := err.(net.Error); ok && !ne.Temporary() {
					log.Printf("Non-temporary accept error, stopping listener.")
					return
				}
				time.Sleep(100 * time.Millisecond) // Small delay before retrying
			}
			continue
		}
		// Handle each connection in a new goroutine
		a.wg.Add(1)
		go a.handleConnection(conn)
	}
}

// handleConnection reads command, processes it, and sends response
func (a *App) handleConnection(conn *net.UnixConn) {
	defer conn.Close()
	defer a.wg.Done()

	// Set a deadline for reading the command
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	decoder := json.NewDecoder(conn)
	encoder := json.NewEncoder(conn)

	var cmd ipc.Command
	if err := decoder.Decode(&cmd); err != nil {
		if err != io.EOF {
			log.Printf("Failed to decode command: %v", err)
		}
		// Send error response even if decoding failed partially
		_ = encoder.Encode(ipc.Response{Success: false, Message: "Failed to decode command: " + err.Error()})
		return
	}

	// Reset read deadline
	conn.SetReadDeadline(time.Time{})
	// Set write deadline for response
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))

	log.Printf("Received command: %s", cmd.Name)

	// Process command
	response := a.processCommand(cmd)

	// Send response
	if err := encoder.Encode(response); err != nil {
		log.Printf("Failed to send response: %v", err)
	}
}

// processCommand routes the command to the correct handler
func (a *App) processCommand(cmd ipc.Command) ipc.Response {
	switch cmd.Name {
	case ipc.CmdPing:
		return ipc.Response{Success: true, Message: "pong"}

	case ipc.CmdStartPomodoro:
		var args ipc.StartPomodoroArgs
		if err := mapToStruct(cmd.Args, &args); err != nil {
			return ipc.Response{Success: false, Message: fmt.Sprintf("Invalid args for %s: %v", cmd.Name, err)}
		}
		if args.State != event.StateFocus && args.State != event.StateShortBreak && args.State != event.StateLongBreak {
			return ipc.Response{Success: false, Message: "Invalid Pomodoro state specified"}
		}
		a.hookMan.SendStartPomodoro(args.State)
		return ipc.Response{Success: true, Message: fmt.Sprintf("Pomodoro started: %s", args.State)}

	case ipc.CmdStopPomodoro:
		a.hookMan.SendStopPomodoro()
		return ipc.Response{Success: true, Message: "Pomodoro stop requested"}

	case ipc.CmdAddEvent:
		var args ipc.AddEventArgs
		if err := mapToStruct(cmd.Args, &args); err != nil {
			return ipc.Response{Success: false, Message: fmt.Sprintf("Invalid args for %s: %v", cmd.Name, err)}
		}
		if args.Tag == "" {
			return ipc.Response{Success: false, Message: "Event tag cannot be empty"}
		}
		newEvent := event.Event{
			Timestamp: time.Now(),
			Type:      event.EventTypeCustom,
			Tag:       args.Tag,
			Notes:     args.Notes,
			Value:     args.Value,
		}
		// Send event to the event processing channel
		select {
		case a.eventChan <- newEvent:
			return ipc.Response{Success: true, Message: fmt.Sprintf("Custom event '%s' added", args.Tag)}
		case <-a.ctx.Done():
			return ipc.Response{Success: false, Message: "App is shutting down"}
		case <-time.After(1 * time.Second):
			return ipc.Response{Success: false, Message: "Timeout adding event"}
		}

	case ipc.CmdAddReminder:
		var args ipc.AddReminderArgs
		if err := mapToStruct(cmd.Args, &args); err != nil {
			return ipc.Response{Success: false, Message: fmt.Sprintf("Invalid args for %s: %v", cmd.Name, err)}
		}
		duration, err := time.ParseDuration(args.Duration)
		if err != nil {
			return ipc.Response{Success: false, Message: fmt.Sprintf("Invalid duration format '%s': %v", args.Duration, err)}
		}
		if args.Message == "" {
			return ipc.Response{Success: false, Message: "Reminder message cannot be empty"}
		}
		a.hookMan.SendAddReminder(duration, args.Message)
		return ipc.Response{Success: true, Message: fmt.Sprintf("Reminder '%s' set for %s", args.Message, duration)}

	case ipc.CmdGetStatus:
		a.statusMutex.RLock()
		status := ipc.StatusData{
			PomodoroState:         a.currentPomoStatus.State,
			PomodoroRemainingSecs: a.currentPomoStatus.RemainingTime.Seconds(),
			PomodoroCycleCount:    a.currentPomoStatus.CycleCount,
		}
		a.statusMutex.RUnlock()
		return ipc.Response{Success: true, Data: status}

	default:
		return ipc.Response{Success: false, Message: fmt.Sprintf("Unknown command: %s", cmd.Name)}
	}
}

// Helper function to convert map[string]interface{} (from json unmarshal) to struct
func mapToStruct(input interface{}, output interface{}) error {
	if input == nil {
		return nil // No args provided, might be okay for some commands
	}
	// Convert map to JSON bytes
	jsonBytes, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("failed to marshal args map: %w", err)
	}
	// Unmarshal JSON bytes into the target struct
	if err := json.Unmarshal(jsonBytes, output); err != nil {
		return fmt.Errorf("failed to unmarshal args into struct: %w", err)
	}
	return nil
}

func (a *App) Run() error {
	defer a.cleanup() // Ensure cleanup runs

	log.Println("Starting QuantMe Application (Daemon Mode)...")
	log.Printf("Config: %+v", a.cfg)
	log.Printf("Collecting metrics: %s", a.cfg.CollectMode)
	if a.x11Col == nil {
		log.Println("X11 focus monitoring: DISABLED")
	} else {
		log.Println("X11 focus monitoring: ENABLED")
	}

	// --- Setup Socket ---
	if err := a.setupSocket(); err != nil {
		log.Fatalf("Failed to set up socket: %v", err)
		// No need to return error here, log.Fatalf exits
	}
	// Defer listener close in cleanup

	// Start signal handling
	a.handleSignals()

	// Start event processor
	a.wg.Add(1)
	go a.processEvents()

	// Start hook manager
	a.hookMan.Start()

	// Start X11 collector if initialized
	if a.x11Col != nil {
		a.wg.Add(1)
		go func() {
			defer a.wg.Done()
			log.Println("Launching X11 collector goroutine")
			collectInterval := time.Duration(a.cfg.CollectionIntervalSeconds) * time.Second
			focusOnly := a.cfg.CollectMode == "focus"
			err := a.x11Col.Start(a.ctx, collectInterval, a.eventChan, focusOnly)
			if err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("X11 collector error: %v", err)
			}
			log.Println("X11 collector goroutine finished.")
		}()
	}

	// Start main application loop (listens for Hook updates, handles internal logic)
	a.wg.Add(1)
	go a.mainLoop()

	// --- Start Socket Listener ---
	a.wg.Add(1)
	go a.listenForCommands()

	// Record App Start event
	_, err := a.storage.SaveEvent(a.ctx, event.Event{Timestamp: time.Now(), Type: event.EventTypeAppStart})
	if err != nil {
		log.Printf("Warning: Failed to save AppStart event: %v", err)
	}

	log.Println("QuantMe daemon running. Send commands via quantme-cli or socket.")
	<-a.ctx.Done() // Block here until context is cancelled

	log.Println("Shutdown signal received, waiting for components...")

	// Close the listener *before* waiting for goroutines to allow accept() to return
	if a.listener != nil {
		log.Println("Closing command socket listener...")
		if err := a.listener.Close(); err != nil {
			log.Printf("Error closing socket listener: %v", err)
		}
	}

	waitChan := make(chan struct{})
	go func() {
		a.wg.Wait()
		close(waitChan)
	}()

	select {
	case <-waitChan:
		log.Println("All application goroutines finished.")
	case <-time.After(5 * time.Second):
		log.Println("Warning: Timeout waiting for application goroutines to stop.")
	}

	log.Println("QuantMe Application finished.")
	return nil
}

// mainLoop handles updates from hooks AND updates internal status
func (a *App) mainLoop() {
	defer a.wg.Done()
	defer log.Println("Main application loop stopped.")

	for {
		select {
		case <-a.ctx.Done():
			return // Exit loop on context cancellation

		case update := <-a.hookUpdateChan:
			// Process updates from HookManager
			switch u := update.(type) {
			case event.PomodoroUpdate:
				log.Printf("Pomodoro State: %s, Remaining: %s, Cycles: %d",
					u.State, formatDuration(u.RemainingTime), u.CycleCount)
				// Update internal status
				a.statusMutex.Lock()
				a.currentPomoStatus = u
				a.statusMutex.Unlock()

			case event.Notification:
				log.Printf("Notification: [%s] %s", u.Title, u.Message)
				// TODO: Optionally add desktop notifications here (e.g., using beeep or dbus)

			case event.Event:
				// Events generated by HookManager (like Pomo state changes)
				log.Printf("Debug: Event from HookManager received in mainLoop (Type: %s)", u.Type)
				// Forward to eventChan
				select {
				case a.eventChan <- u:
					// log.Printf("Forwarded HookManager event (Type: %s) to eventChan", u.Type)
				case <-a.ctx.Done():
				case <-time.After(50 * time.Millisecond):
					log.Printf("Warning: Timeout forwarding HookManager event (Type: %s)", u.Type)
				}

			default:
				log.Printf("Unknown update type from HookManager: %T", u)
			}
		}
	}
}

// processEvents remains largely the same
func (a *App) processEvents() {
	defer a.wg.Done()
	defer log.Println("Event processor stopped.")

	var lastFocusEvent *event.Event

	for {
		select {
		case <-a.ctx.Done():
			log.Println("Event processor shutting down.")
			return
		case e := <-a.eventChan:
			isFocusActive := a.hookMan.GetCurrentState() == event.StateFocus
			shouldCollect := a.cfg.CollectMode == "always" || (a.cfg.CollectMode == "focus" && isFocusActive)

			isManualOrMetaEvent := e.Type == event.EventTypeCustom || e.Type == event.EventTypePomodoro ||
				e.Type == event.EventTypeAppStart || e.Type == event.EventTypeAppStop

			// Always save manual/meta events. Save collector events based on mode.
			if isManualOrMetaEvent || (e.Type == event.EventTypeFocusChange && shouldCollect) {
				if e.Type == event.EventTypeFocusChange {
					// Log focus change instead of TUI update
					log.Printf("Focus Changed: App='%s', Title='%s'", e.AppName, Truncate(e.WindowTitle, 80))

					if lastFocusEvent != nil && lastFocusEvent.Type == event.EventTypeFocusChange {
						duration := e.Timestamp.Sub(lastFocusEvent.Timestamp)
						log.Printf("Time on '%s': %s", lastFocusEvent.AppName, formatDuration(duration))
						// Optional: Save duration event here
					}
					lastFocusEvent = &e
				}

				// Save the event
				_, err := a.storage.SaveEvent(a.ctx, e)
				if err != nil {
					log.Printf("Error saving event (Type: %s, Tag: %s): %v", e.Type, e.Tag, err)
				} else {
					// Log successful save only for non-focus events to reduce noise
					if e.Type != event.EventTypeFocusChange {
						log.Printf("Event saved: Type=%s, Tag=%s, Notes=%s", e.Type, e.Tag, e.Notes)
					}
				}
			} else {
				// Log skipped events if needed (e.g., focus changes when not in focus mode)
				// log.Printf("Skipping event save (Type: %s) due to collect_mode/state", e.Type)
			}
		}
	}
}

// handleSignals remains the same
func (a *App) handleSignals() {
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigChan
		log.Printf("Received signal: %v. Initiating shutdown...", sig)
		a.cancel() // Trigger context cancellation for graceful shutdown
	}()
}

// cleanup needs to ensure socket removal
func (a *App) cleanup() {
	log.Println("Running cleanup...")

	// Record App Stop event
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer saveCancel()
	_, err := a.storage.SaveEvent(saveCtx, event.Event{Timestamp: time.Now(), Type: event.EventTypeAppStop})
	if err != nil {
		log.Printf("Warning: Failed to save AppStop event: %v", err)
	}

	// Stop components
	if a.x11Col != nil {
		if err := a.x11Col.Stop(); err != nil {
			log.Printf("Error stopping X11 collector: %v", err)
		}
	}
	if a.hookMan != nil {
		a.hookMan.Stop()
	}

	// Close storage
	if a.storage != nil {
		if err := a.storage.Close(); err != nil {
			log.Printf("Error closing storage: %v", err)
		}
	}

	// --- Remove Socket File ---
	// Note: Listener is closed in Run() before wg.Wait()
	if _, err := os.Stat(a.socketPath); err == nil {
		log.Printf("Removing socket file: %s", a.socketPath)
		if err := os.Remove(a.socketPath); err != nil {
			log.Printf("Warning: Failed to remove socket file %s: %v", a.socketPath, err)
		}
	}

	log.Println("Cleanup finished.")
}

// --- Helper Functions (formatDuration, Truncate) remain the same ---

func formatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute
	d -= m * time.Minute
	s := d / time.Second
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func Truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
