package ipc

import "quantme/internal/event"

const SocketPath = "/tmp/quantme.sock"

// Command represents a command sent over the socket
type Command struct {
	Name string      `json:"name"`
	Args interface{} `json:"args,omitempty"`
}

// Response represents a response sent back over the socket
type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"` // Optional data in response
}

// --- Command Argument Structs ---

type StartPomodoroArgs struct {
	State event.PomodoroState `json:"state"` // Focus, ShortBreak, LongBreak
}

// StopPomodoroArgs - No arguments needed

type AddEventArgs struct {
	Tag   string  `json:"tag"`
	Notes string  `json:"notes"`
	Value float64 `json:"value"` // Optional value
	// Type is implicitly event.EventTypeCustom for this command
}

type AddReminderArgs struct {
	Duration string `json:"duration"` // e.g., "5m", "1h"
	Message  string `json:"message"`
}

// --- Command Names (Constants) ---

const (
	CmdStartPomodoro = "start_pomodoro"
	CmdStopPomodoro  = "stop_pomodoro"
	CmdAddEvent      = "add_event"
	CmdAddReminder   = "add_reminder"
	CmdGetStatus     = "get_status" // Example: Get current pomodoro state
	CmdPing          = "ping"       // Simple health check
)

// --- Status Response Data ---
type StatusData struct {
	PomodoroState         event.PomodoroState `json:"pomodoro_state"`
	PomodoroRemainingSecs float64             `json:"pomodoro_remaining_secs"`
	PomodoroCycleCount    int                 `json:"pomodoro_cycle_count"`
}
