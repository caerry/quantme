package event

import "time"

type EventType string

const (
	EventTypeFocusChange EventType = "focus_change"
	EventTypeKeyPress    EventType = "keypress_count" // Placeholder
	EventTypeCustom      EventType = "custom"
	EventTypePomodoro    EventType = "pomodoro_state"
	EventTypeAppStart    EventType = "app_start"
	EventTypeAppStop     EventType = "app_stop"
	// Add more as needed (e.g., EventTypeReminder)
)

// Event structure to store in DB
type Event struct {
	ID          int64     `db:"id"`
	Timestamp   time.Time `db:"timestamp"`
	Type        EventType `db:"type"`
	AppName     string    `db:"app_name"`     // For focus_change
	WindowTitle string    `db:"window_title"` // For focus_change
	Value       float64   `db:"value"`        // Generic value (e.g., duration for pomodoro)
	Tag         string    `db:"tag"`          // e.g., Pomodoro State (Focus, Break), Custom event tag
	Notes       string    `db:"notes"`        // For custom events or pomodoro cycle info
}

// Pomodoro State
type PomodoroState string

const (
	StateIdle       PomodoroState = "Idle"
	StateFocus      PomodoroState = "Focus"
	StateShortBreak PomodoroState = "ShortBreak"
	StateLongBreak  PomodoroState = "LongBreak"
)

// Used for communication channels
type FocusInfo struct {
	AppName string
	Title   string
}

type PomodoroUpdate struct {
	State         PomodoroState
	RemainingTime time.Duration
	CycleCount    int // Number of focus sessions completed in current set
}

type Notification struct {
	Title   string
	Message string
}
