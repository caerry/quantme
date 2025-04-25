package main

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"quantme/internal/event"
	"quantme/internal/ipc"

	sqlitestore "quantme/internal/storage/sqlite"

	"github.com/spf13/cobra"
)

var dbPath string

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

type ReportData struct {
	GeneratedAt         string
	StartDate           string
	EndDate             string
	AppTimeJSON         template.JS // Use template.JS to prevent escaping
	TimelineJSON        template.JS
	CustomEventsJSON    template.JS
	TotalPomodoroCycles int
}

type ChartData struct {
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
}

type TimelineEvent struct {
	TS    string          `json:"ts"` // ISO 8601 format for JS
	Type  event.EventType `json:"type"`
	App   string          `json:"app,omitempty"`
	Title string          `json:"title,omitempty"`
	Tag   string          `json:"tag,omitempty"`
	Notes string          `json:"notes,omitempty"`
	Value float64         `json:"value,omitempty"`
}

func openBrowser(url string) error {
	var cmd string
	var args []string

	switch runtime.GOOS {
	case "windows":
		cmd = "cmd"
		args = []string{"/c", "start"}
	case "darwin":
		cmd = "open"
	default: // "linux", "freebsd", "openbsd", "netbsd"
		cmd = "xdg-open"
	}
	args = append(args, url)
	return exec.Command(cmd, args...).Start()
}

// Report Command Group
var reportCmd = &cobra.Command{
	Use:   "report",
	Short: "Generate reports from QuantMe data",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		// Ensure dbPath is determined before subcommands run
		rootCmd.PersistentPreRun(cmd, args)
		// Check if DB file exists before trying to generate report
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			log.Fatalf("Error: Database file not found at %s. Ensure quantme daemon has run or specify path with --db.", dbPath)
		} else if err != nil {
			log.Fatalf("Error accessing database file %s: %v", dbPath, err)
		}
	},
}

var reportGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate an HTML activity report",
	Run: func(cmd *cobra.Command, args []string) {
		days, _ := cmd.Flags().GetInt("days")
		outputFile, _ := cmd.Flags().GetString("output")
		openReport, _ := cmd.Flags().GetBool("open")

		// Calculate time range
		endTime := time.Now()
		startTime := endTime.AddDate(0, 0, -days) // Go back 'days' days

		log.Printf("Generating report for %d days (%s to %s)", days, startTime.Format("2006-01-02"), endTime.Format("2006-01-02"))
		log.Printf("Using database: %s", dbPath)

		// Initialize storage
		store := sqlitestore.NewSQLiteStore(dbPath)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // Context for DB operations
		defer cancel()

		// NOTE: We are calling Init just to open the DB connection via sql.Open.
		// This doesn't run the table creation logic if the DB exists.
		// A cleaner approach might be to refactor storage to have an Open() method.
		if err := store.Init(ctx); err != nil {
			log.Fatalf("Failed to initialize storage connection: %v", err)
		}
		defer store.Close()

		// Fetch all relevant events
		events, err := store.GetEvents(ctx, startTime, endTime,
			event.EventTypeFocusChange,
			event.EventTypeCustom,
			event.EventTypePomodoro,
			event.EventTypeAppStart,
			event.EventTypeAppStop,
		)
		if err != nil {
			log.Fatalf("Failed to fetch events: %v", err)
		}

		log.Printf("Fetched %d events for report.", len(events))
		if len(events) == 0 {
			log.Println("No event data found for the specified period.")
			// Optionally generate an empty report or just exit
			fmt.Println("No data to generate report.")
			return
		}

		// Process data
		appTime := make(map[string]time.Duration)
		var timelineEvents []TimelineEvent
		customEventCounts := make(map[string]int)
		pomoCycles := 0
		var lastFocusTime time.Time = startTime // Start duration calc from beginning of period

		// Sort events by timestamp just in case DB didn't guarantee it
		sort.Slice(events, func(i, j int) bool {
			return events[i].Timestamp.Before(events[j].Timestamp)
		})

		if len(events) > 0 && events[0].Type == event.EventTypeFocusChange {
			lastFocusTime = events[0].Timestamp // If first event is focus, start from there
		}

		for i, e := range events {
			// Timeline data
			timelineEvents = append(timelineEvents, TimelineEvent{
				TS:    e.Timestamp.Format(time.RFC3339), // ISO 8601 for JS
				Type:  e.Type,
				App:   e.AppName,
				Title: e.WindowTitle,
				Tag:   e.Tag,
				Notes: e.Notes,
				Value: e.Value,
			})

			// App time calculation (based on focus change)
			if e.Type == event.EventTypeFocusChange {
				// Calculate duration of the *previous* focus
				// Find previous focus change event or start time
				prevFocusTime := startTime
				prevAppName := "Unknown/Idle"
				for j := i - 1; j >= 0; j-- {
					if events[j].Type == event.EventTypeFocusChange {
						prevFocusTime = events[j].Timestamp
						prevAppName = events[j].AppName
						break // Found the immediate previous focus
					}
					// If we hit AppStart without focus change, use start time? Needs refinement.
				}
				// Use AppName from the previous event for duration calculation
				if prevAppName != "Unknown/Idle" && !e.Timestamp.Before(prevFocusTime) {
					duration := e.Timestamp.Sub(prevFocusTime)
					appTime[prevAppName] += duration
				}
				lastFocusTime = e.Timestamp // Update last focus time for next calc or end calc
			}

			// Custom event counts
			if e.Type == event.EventTypeCustom {
				customEventCounts[e.Tag]++
			}

			// Pomodoro cycle count (count end of Focus state)
			if e.Type == event.EventTypePomodoro && event.PomodoroState(e.Tag) == event.StateFocus {
				// Look ahead to see if this focus session completed within the time range
				// This is tricky. A simpler approach is to count the *start* of focus cycles
				// or count *start* of break cycles. Let's count focus starts for simplicity.
				pomoCycles++
			}
		}

		// Add time for the last focused app until the end of the period
		var lastAppName = "Unknown/Idle"
		for i := len(events) - 1; i >= 0; i-- {
			if events[i].Type == event.EventTypeFocusChange {
				lastAppName = events[i].AppName
				lastFocusTime = events[i].Timestamp
				break
			}
		}
		if lastAppName != "Unknown/Idle" && endTime.After(lastFocusTime) {
			duration := endTime.Sub(lastFocusTime)
			appTime[lastAppName] += duration
		}

		// Prepare data for charts
		appTimeChart := ChartData{Labels: make([]string, 0), Values: make([]float64, 0)}
		for app, duration := range appTime {
			appTimeChart.Labels = append(appTimeChart.Labels, app)
			appTimeChart.Values = append(appTimeChart.Values, duration.Minutes())
		}

		customEventChart := ChartData{Labels: make([]string, 0), Values: make([]float64, 0)}
		for tag, count := range customEventCounts {
			customEventChart.Labels = append(customEventChart.Labels, tag)
			customEventChart.Values = append(customEventChart.Values, float64(count))
		}

		// Sort chart data for better presentation
		sort.Strings(appTimeChart.Labels) // Sort app names alphabetically
		// Reorder values accordingly (could be improved with a struct slice)
		reorderedAppValues := make([]float64, len(appTimeChart.Labels))
		originalMap := make(map[string]float64)
		for _, label := range appTimeChart.Labels {
			for app, duration := range appTime {
				if app == label {
					originalMap[label] = duration.Minutes()
					break
				}
			}
		}
		for i, label := range appTimeChart.Labels {
			reorderedAppValues[i] = originalMap[label]
		}
		appTimeChart.Values = reorderedAppValues

		sort.Strings(customEventChart.Labels) // Sort tags alphabetically
		reorderedEventValues := make([]float64, len(customEventChart.Labels))
		originalEventMap := make(map[string]float64)
		for _, label := range customEventChart.Labels {
			for tag, count := range customEventCounts {
				if tag == label {
					originalEventMap[label] = float64(count)
					break
				}
			}
		}
		for i, label := range customEventChart.Labels {
			reorderedEventValues[i] = originalEventMap[label]
		}
		customEventChart.Values = reorderedEventValues

		// Marshal data to JSON for embedding
		appJson, _ := json.Marshal(appTimeChart)
		timelineJson, _ := json.Marshal(timelineEvents)
		customJson, _ := json.Marshal(customEventChart)

		// Prepare data for template execution
		reportData := ReportData{
			GeneratedAt:         time.Now().Format("2006-01-02 15:04:05"),
			StartDate:           startTime.Format("2006-01-02"),
			EndDate:             endTime.Format("2006-01-02"),
			AppTimeJSON:         template.JS(appJson),
			TimelineJSON:        template.JS(timelineJson),
			CustomEventsJSON:    template.JS(customJson),
			TotalPomodoroCycles: pomoCycles, // Use the calculated count
		}

		// Find the template file relative to the executable or CWD
		// This makes it work better when installed/run from different locations
		exePath, err := os.Executable()
		if err != nil {
			log.Printf("Warning: Cannot determine executable path: %v. Looking for template in CWD.", err)
			exePath = "." // Fallback to current working directory
		}
		templatePath := filepath.Join(filepath.Dir(exePath), "template.html")
		if _, err := os.Stat(templatePath); os.IsNotExist(err) {
			// If not found near executable, try current working directory
			templatePath = "template.html"
			if _, err := os.Stat(templatePath); os.IsNotExist(err) {
				log.Fatalf("Error: template.html not found near executable or in current directory.")
			}
		}

		// Parse and execute template
		tmpl, err := template.ParseFiles(templatePath)
		if err != nil {
			log.Fatalf("Failed to parse template file (%s): %v", templatePath, err)
		}

		f, err := os.Create(outputFile)
		if err != nil {
			log.Fatalf("Failed to create output file (%s): %v", outputFile, err)
		}
		defer f.Close()

		err = tmpl.Execute(f, reportData)
		if err != nil {
			log.Fatalf("Failed to execute template: %v", err)
		}

		log.Printf("Report successfully generated: %s", outputFile)

		// Open in browser if requested
		if openReport {
			absPath, _ := filepath.Abs(outputFile)
			url := "file://" + absPath
			log.Printf("Opening report in browser: %s", url)
			if err := openBrowser(url); err != nil {
				log.Printf("Warning: Failed to open report in browser: %v", err)
			}
		}
	},
}

func main() {
	// --- Pomodoro Commands ---
	pomodoroStartCmd.Flags().StringP("state", "s", "Focus", "Pomodoro state to start (Focus, ShortBreak, LongBreak)")
	rootCmd.PersistentFlags().StringVar(&dbPath, "db", "", "Path to the QuantMe database file (default: loaded from config or 'quantme.db')")

	reportGenerateCmd.Flags().IntP("days", "d", 7, "Number of past days to include in the report")
	reportGenerateCmd.Flags().StringP("output", "o", "quantme_report.html", "Output HTML file name")
	reportGenerateCmd.Flags().BoolP("open", "O", false, "Open the generated report in the default browser")
	reportCmd.AddCommand(reportGenerateCmd)
	rootCmd.AddCommand(reportCmd)

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
