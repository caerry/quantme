package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"quantme/internal/event"
	"quantme/internal/ipc"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "quantme-cli",
	Short: "CLI tool to interact with the QuantMe daemon",
	Long:  `A command-line interface to send commands (like starting Pomodoro, adding events) to the running QuantMe daemon via its Unix socket.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
	},
}

// --- Client Helper Function ---
func sendCommand(cmd ipc.Command) {
	conn, err := net.DialTimeout("unix", ipc.SocketPath, 2*time.Second)
	if err != nil {
		log.Fatalf("Error connecting to daemon socket (%s): %v\nIs the QuantMe daemon running?", ipc.SocketPath, err)
	}
	defer conn.Close()

	// Set deadlines
	conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	conn.SetReadDeadline(time.Now().Add(5 * time.Second)) // For response

	encoder := json.NewEncoder(conn)
	decoder := json.NewDecoder(conn)

	// Send command
	if err := encoder.Encode(cmd); err != nil {
		log.Fatalf("Error sending command: %v", err)
	}

	// Receive response
	var resp ipc.Response
	if err := decoder.Decode(&resp); err != nil {
		log.Fatalf("Error receiving response: %v", err)
	}

	// Print response
	if resp.Success {
		fmt.Println("Success:", resp.Message)
		if resp.Data != nil {
			// Pretty print JSON data if available
			prettyData, err := json.MarshalIndent(resp.Data, "", "  ")
			if err == nil {
				fmt.Println("Data:")
				fmt.Println(string(prettyData))
			} else {
				fmt.Println("Data (raw):", resp.Data)
			}
		}
	} else {
		fmt.Fprintf(os.Stderr, "Error: %s\n", resp.Message)
		os.Exit(1) // Exit with error code if command failed server-side
	}
}

// --- Command Definitions ---

// Ping Command
var pingCmd = &cobra.Command{
	Use:   "ping",
	Short: "Check if the QuantMe daemon is running",
	Run: func(cmd *cobra.Command, args []string) {
		sendCommand(ipc.Command{Name: ipc.CmdPing})
	},
}

// Pomodoro Command Group
var pomodoroCmd = &cobra.Command{
	Use:   "pomodoro",
	Short: "Control the Pomodoro timer",
}

var pomodoroStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start a Pomodoro session (Focus, ShortBreak, LongBreak)",
	Run: func(cmd *cobra.Command, args []string) {
		stateStr, _ := cmd.Flags().GetString("state")
		var state event.PomodoroState
		switch stateStr {
		case "Focus", "focus":
			state = event.StateFocus
		case "ShortBreak", "short":
			state = event.StateShortBreak
		case "LongBreak", "long":
			state = event.StateLongBreak
		default:
			log.Fatalf("Invalid state: %s. Use 'Focus', 'ShortBreak', or 'LongBreak'", stateStr)
		}
		sendCommand(ipc.Command{
			Name: ipc.CmdStartPomodoro,
			Args: ipc.StartPomodoroArgs{State: state},
		})
	},
}

var pomodoroStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the current Pomodoro timer",
	Run: func(cmd *cobra.Command, args []string) {
		sendCommand(ipc.Command{Name: ipc.CmdStopPomodoro})
	},
}

var eventCmd = &cobra.Command{
	Use:   "event",
	Short: "Manage custom events",
}

var eventAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a custom event",
	Run: func(cmd *cobra.Command, args []string) {
		tag, _ := cmd.Flags().GetString("tag")
		notes, _ := cmd.Flags().GetString("notes")
		value, _ := cmd.Flags().GetFloat64("value")

		if tag == "" {
			log.Fatal("Error: --tag flag is required")
		}

		sendCommand(ipc.Command{
			Name: ipc.CmdAddEvent,
			Args: ipc.AddEventArgs{
				Tag:   tag,
				Notes: notes,
				Value: value,
			},
		})
	},
}

var reminderCmd = &cobra.Command{
	Use:   "reminder",
	Short: "Manage reminders",
}

var reminderAddCmd = &cobra.Command{
	Use:   "add",
	Short: "Add a reminder (e.g., '5m', '1h30m')",
	Run: func(cmd *cobra.Command, args []string) {
		duration, _ := cmd.Flags().GetString("duration")
		message, _ := cmd.Flags().GetString("message")

		if duration == "" {
			log.Fatal("Error: --duration flag is required (e.g., '5m', '1h')")
		}
		if message == "" {
			log.Fatal("Error: --message flag is required")
		}

		// Basic validation of duration format (more robust parsing is in daemon)
		_, err := time.ParseDuration(duration)
		if err != nil {
			log.Fatalf("Error: Invalid duration format for --duration: %v", err)
		}

		sendCommand(ipc.Command{
			Name: ipc.CmdAddReminder,
			Args: ipc.AddReminderArgs{
				Duration: duration,
				Message:  message,
			},
		})
	},
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Get the current status from the daemon (e.g., Pomodoro state)",
	Run: func(cmd *cobra.Command, args []string) {
		sendCommand(ipc.Command{Name: ipc.CmdGetStatus})
	},
}

func main() {
	// --- Pomodoro Commands ---
	pomodoroStartCmd.Flags().StringP("state", "s", "Focus", "Pomodoro state to start (Focus, ShortBreak, LongBreak)")
	pomodoroCmd.AddCommand(pomodoroStartCmd)
	pomodoroCmd.AddCommand(pomodoroStopCmd)
	rootCmd.AddCommand(pomodoroCmd)

	// --- Event Commands ---
	eventAddCmd.Flags().StringP("tag", "t", "", "Tag for the custom event (required)")
	eventAddCmd.Flags().StringP("notes", "n", "", "Optional notes for the custom event")
	eventAddCmd.Flags().Float64P("value", "v", 0, "Optional numeric value for the custom event")
	eventAddCmd.MarkFlagRequired("tag") // Enforce tag
	eventCmd.AddCommand(eventAddCmd)
	rootCmd.AddCommand(eventCmd)

	// --- Reminder Commands ---
	reminderAddCmd.Flags().StringP("duration", "d", "", "Duration until reminder (e.g., '5m', '1h', '10s') (required)")
	reminderAddCmd.Flags().StringP("message", "m", "", "Reminder message (required)")
	reminderAddCmd.MarkFlagRequired("duration")
	reminderAddCmd.MarkFlagRequired("message")
	reminderCmd.AddCommand(reminderAddCmd)
	rootCmd.AddCommand(reminderCmd)

	// --- Other Commands ---
	rootCmd.AddCommand(pingCmd)
	rootCmd.AddCommand(statusCmd)

	// --- Execute ---
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error executing command: %v\n", err)
		os.Exit(1)
	}
}
