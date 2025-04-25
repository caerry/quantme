package hooks

import (
	"context"
	"fmt"
	"log"
	"quantme/internal/config"
	"quantme/internal/event"
	"time"
)

type HookManager struct {
	cfg          *config.Config
	pomodoroCfg  config.PomodoroConfig
	currentState event.PomodoroState
	cycleCount   int // Pomodoro focus cycles completed

	timer        *time.Timer        // Active timer (Pomodoro or custom)
	timerEndTime time.Time          // When the current timer is expected to end
	timerStop    chan struct{}      // To stop the current timer prematurely
	cmdChan      chan interface{}   // Channel for commands (start/stop pomodoro, add hook)
	updateChan   chan<- interface{} // Channel to send updates (state changes, notifications) back to app

	ctx    context.Context
	cancel context.CancelFunc
}

// --- Command Types ---
type StartPomodoroCmd struct {
	State event.PomodoroState // Which state to start (Focus, ShortBreak, LongBreak)
}
type StopPomodoroCmd struct{}
type AddReminderCmd struct {
	Duration time.Duration
	Message  string
	// Add a field to store the message for when the timer expires
	callbackMessage string
}

// --- Update Types ---
// Use event.PomodoroUpdate
// Use event.Notification

func NewHookManager(cfg *config.Config, updateChan chan<- interface{}) *HookManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &HookManager{
		cfg:          cfg,
		pomodoroCfg:  cfg.Pomodoro,
		currentState: event.StateIdle,
		cmdChan:      make(chan interface{}, 10), // Buffered channel
		updateChan:   updateChan,
		ctx:          ctx,
		cancel:       cancel,
	}
}

func (hm *HookManager) Start() {
	log.Println("Starting Hook Manager")
	go hm.runLoop()
}

func (hm *HookManager) Stop() {
	log.Println("Stopping Hook Manager")
	hm.cancel()
}

// Public methods to send commands
func (hm *HookManager) SendStartPomodoro(state event.PomodoroState) {
	select {
	case hm.cmdChan <- StartPomodoroCmd{State: state}:
	case <-hm.ctx.Done():
	}
}

func (hm *HookManager) SendStopPomodoro() {
	select {
	case hm.cmdChan <- StopPomodoroCmd{}:
	case <-hm.ctx.Done():
	}
}

func (hm *HookManager) SendAddReminder(duration time.Duration, message string) {
	select {
	// Pass the message along in the command itself
	case hm.cmdChan <- AddReminderCmd{Duration: duration, Message: message, callbackMessage: message}:
	case <-hm.ctx.Done():
	}
}

func (hm *HookManager) runLoop() {
	defer log.Println("Hook Manager loop stopped.")

	// Ticker for periodic state updates (e.g., remaining time)
	updateTicker := time.NewTicker(5 * time.Second) // Update less frequently than 1s for CLI
	defer updateTicker.Stop()

	for {
		var timerChan <-chan time.Time
		if hm.timer != nil {
			timerChan = hm.timer.C
		}

		// Calculate remaining time for periodic updates
		var remaining time.Duration
		if !hm.timerEndTime.IsZero() {
			remaining = time.Until(hm.timerEndTime)
			if remaining < 0 {
				remaining = 0
			}
		}

		select {
		case <-hm.ctx.Done():
			hm.stopActiveTimer()
			return

		case cmd := <-hm.cmdChan:
			hm.handleCommand(cmd) // This now sets timerEndTime correctly

		case <-timerChan:
			hm.timer = nil // Timer fired
			expiredEndTime := hm.timerEndTime
			hm.timerEndTime = time.Time{}        // Reset end time
			hm.handleTimerExpiry(expiredEndTime) // Pass the intended end time

		case <-updateTicker.C:
			// Send periodic Pomodoro update if active
			if hm.currentState != event.StateIdle {
				hm.sendUpdate(event.PomodoroUpdate{
					State:         hm.currentState,
					RemainingTime: remaining, // Use calculated remaining time
					CycleCount:    hm.cycleCount,
				})
			}

		case <-hm.timerStop:
			// Timer was stopped externally (e.g., by StopPomodoroCmd)
			hm.timer = nil
			hm.timerEndTime = time.Time{} // Reset end time
			log.Println("Active timer stopped externally.")
		}
	}
}

func (hm *HookManager) handleCommand(cmd interface{}) {
	switch c := cmd.(type) {
	case StartPomodoroCmd:
		log.Printf("HookMgr Command: Start Pomodoro (%s)", c.State)
		hm.stopActiveTimer() // Stop any existing timer

		var duration time.Duration
		newState := c.State

		switch newState {
		case event.StateFocus:
			duration = hm.pomodoroCfg.FocusDuration()
		case event.StateShortBreak:
			duration = hm.pomodoroCfg.ShortBreakDuration()
		case event.StateLongBreak:
			duration = hm.pomodoroCfg.LongBreakDuration()
		default:
			log.Printf("Warning: Invalid state requested for Pomodoro start: %s", c.State)
			hm.currentState = event.StateIdle
			hm.sendUpdate(event.PomodoroUpdate{State: hm.currentState, CycleCount: hm.cycleCount})
			return
		}

		hm.currentState = newState
		hm.timer = time.NewTimer(duration)
		hm.timerStop = make(chan struct{})         // Create a new stop channel for this timer
		hm.timerEndTime = time.Now().Add(duration) // **** SET End Time ****

		log.Printf("HookMgr Timer set for %s (%s), ends at %s", hm.currentState, duration, hm.timerEndTime.Format(time.Kitchen))
		// Send immediate update
		hm.sendUpdate(event.PomodoroUpdate{State: hm.currentState, RemainingTime: duration, CycleCount: hm.cycleCount})
		hm.sendUpdate(event.Notification{Title: "Pomodoro Started", Message: fmt.Sprintf("%s started (%s)", hm.currentState, duration)})

		// Send Pomodoro event to be stored
		hm.sendUpdate(event.Event{
			Timestamp: time.Now(),
			Type:      event.EventTypePomodoro,
			Tag:       string(hm.currentState),
			Value:     duration.Minutes(),
			Notes:     fmt.Sprintf("Cycle %d", hm.cycleCount),
		})

	case StopPomodoroCmd:
		log.Println("HookMgr Command: Stop Pomodoro")
		if hm.currentState != event.StateIdle {
			hm.stopActiveTimer()
			hm.currentState = event.StateIdle
			hm.cycleCount = 0 // Reset cycle count on manual stop
			hm.sendUpdate(event.PomodoroUpdate{State: hm.currentState, CycleCount: hm.cycleCount})
			hm.sendUpdate(event.Notification{Title: "Pomodoro Stopped", Message: "Timer cancelled."})
		} else {
			log.Println("HookMgr: Stop command received but already Idle.")
		}

	case AddReminderCmd:
		log.Printf("HookMgr Command: Add Reminder ('%s' in %s)", c.Message, c.Duration)
		hm.stopActiveTimer()
		hm.currentState = event.StateIdle // Reminder is not a pomodoro state
		hm.timer = time.NewTimer(c.Duration)
		hm.timerStop = make(chan struct{})
		hm.timerEndTime = time.Now().Add(c.Duration) // **** SET End Time ****

		// Store reminder message within the manager to retrieve on expiry
		// This is simplistic; a real system might need a map[time.Time]string
		// For now, we pass it back via handleTimerExpiry logic
		hm.timer.Reset(c.Duration) // Ensure timer is set (NewTimer might be sufficient)

		log.Printf("HookMgr Reminder timer set for %s, ends at %s", c.Duration, hm.timerEndTime.Format(time.Kitchen))
		hm.sendUpdate(event.Notification{Title: "Reminder Set", Message: fmt.Sprintf("'%s' in %s", c.Message, c.Duration)})

	default:
		log.Printf("Warning: Unknown command received in HookManager: %T", c)
	}
}

// handleTimerExpiry now receives the expected end time to differentiate timers
func (hm *HookManager) handleTimerExpiry(expectedEndTime time.Time) {
	log.Printf("HookMgr Timer expired (expected end: %s)", expectedEndTime.Format(time.Kitchen))

	// Retrieve the state that *should* have just finished
	// This relies on currentState not being changed between timer firing and this handler
	expiredState := hm.currentState

	// Logic to determine if it was a Pomodoro state or a reminder
	// This is still a bit implicit based on currentState. A more robust
	// way would be to store metadata with the timer or endTime.
	switch expiredState {
	case event.StateFocus:
		hm.cycleCount++
		nextState := event.StateShortBreak
		if hm.cycleCount > 0 && hm.cycleCount%hm.pomodoroCfg.LongBreakInterval == 0 {
			nextState = event.StateLongBreak
		}
		hm.sendUpdate(event.Notification{Title: "Pomodoro", Message: "Focus session complete!"})
		hm.SendStartPomodoro(nextState) // Auto-start next state

	case event.StateShortBreak, event.StateLongBreak:
		nextState := event.StateFocus
		hm.sendUpdate(event.Notification{Title: "Pomodoro", Message: "Break finished! Time for focus."})
		hm.SendStartPomodoro(nextState) // Auto-start next focus

	default: // Assumed to be a reminder or unexpected state
		hm.currentState = event.StateIdle
		// How to get the reminder message back?
		// Simplistic approach: Assume the last AddReminderCmd set the expectation.
		// This is fragile. A better way: store map[*time.Timer]ReminderInfo or similar.
		reminderMsg := "Reminder!" // Default if logic fails
		// A slightly better HACK: search recent logs? No.
		// Best approach for now: Send a generic notification.
		log.Println("HookMgr Reminder timer expired.")
		hm.sendUpdate(event.Notification{Title: "Reminder", Message: reminderMsg})
		hm.sendUpdate(event.PomodoroUpdate{State: hm.currentState, CycleCount: hm.cycleCount}) // Update status to Idle
	}
}

func (hm *HookManager) stopActiveTimer() {
	if hm.timer != nil {
		if !hm.timer.Stop() {
			// Timer already fired or stopped, try draining channel if needed
			select {
			case <-hm.timer.C:
			default:
			}
		}
		// Signal any goroutine waiting on timerStop (if applicable)
		// If timerStop is primarily for the runLoop, closing it here is correct.
		if hm.timerStop != nil {
			close(hm.timerStop)
			hm.timerStop = nil // Avoid double close
		}
		hm.timer = nil
		hm.timerEndTime = time.Time{} // Reset end time
		log.Println("HookMgr Stopped active timer.")
	}
}

func (hm *HookManager) sendUpdate(update interface{}) {
	select {
	case hm.updateChan <- update:
	case <-hm.ctx.Done():
		// log.Println("HookMgr Context cancelled, cannot send update")
	case <-time.After(100 * time.Millisecond): // Add timeout to prevent blocking app
		log.Println("Warning: Timeout sending update from HookManager")
	}
}

func (hm *HookManager) GetCurrentState() event.PomodoroState {
	// Direct access should be fine as it's read by App goroutine after updates
	// are sent via channel, and written only by HookManager goroutine.
	return hm.currentState
}
