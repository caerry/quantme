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

type AppFocusSpan struct {
	AppName   string `json:"app"`
	StartTime string `json:"start"` // ISO 8601
	EndTime   string `json:"end"`   // ISO 8601
}

// TimelineEvent represents discrete events on the timeline
type TimelineEvent struct {
	Timestamp string          `json:"ts"` // ISO 8601
	Type      event.EventType `json:"type"`
	Tag       string          `json:"tag,omitempty"` // Pomodoro state, Custom tag
	Notes     string          `json:"notes,omitempty"`
	Value     float64         `json:"value,omitempty"`
	// We'll add the AppName that was focused during this event for plotting
	FocusApp string `json:"focusApp,omitempty"`
}

// BarChartData for simple horizontal bar charts
type BarChartData struct {
	Labels []string  `json:"labels"`
	Values []float64 `json:"values"`
}

// PomodoroSummary holds calculated pomodoro stats
type PomodoroSummary struct {
	TotalCycles      int           `json:"totalCycles"`
	TotalFocusTime   time.Duration `json:"-"` // For internal calc
	TotalBreakTime   time.Duration `json:"-"` // For internal calc
	FocusTimeStr     string        `json:"focusTimeStr"`
	BreakTimeStr     string        `json:"breakTimeStr"`
	Efficiency       float64       `json:"efficiency"` // Focus / (Focus + Break) * 100
	FocusEvents      int           `json:"-"`          // Internal counter
	ShortBreakEvents int           `json:"-"`          // Internal counter
	LongBreakEvents  int           `json:"-"`          // Internal counter
}

// Data structure for the main HTML template
type ReportData struct {
	GeneratedAt        string
	StartDate          string
	EndDate            string
	FocusSpansJSON     template.JS       // For Gantt-like bars
	TimelineEventsJSON template.JS       // For scatter points
	AppSummaryJSON     template.JS       // For App time summary bar chart
	CustomEventsJSON   template.JS       // For Custom event summary bar chart
	PomodoroStats      PomodoroSummary   // Pomodoro summary stats
	AppCategoryMap     map[string]string // Maps AppName to a consistent label/category for Y-axis
	AppCategoryJSON    template.JS       // JSON version of the category map for JS
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

		endTime := time.Now()
		startTime := endTime.AddDate(0, 0, -days)

		log.Printf("Generating report for %d days (%s to %s)", days, startTime.Format("2006-01-02"), endTime.Format("2006-01-02"))
		log.Printf("Using database: %s", dbPath)

		// Initialize storage
		store := sqlitestore.NewSQLiteStore(dbPath)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := store.Init(ctx); err != nil {
			log.Fatalf("Failed to initialize storage connection: %v", err)
		}
		defer store.Close()

		// Fetch all relevant events
		allEvents, err := store.GetEvents(ctx, startTime, endTime) // Fetch all types first
		if err != nil {
			log.Fatalf("Failed to fetch events: %v", err)
		}
		if len(allEvents) == 0 {
			fmt.Println("No event data found for the specified period.")
			return
		}
		log.Printf("Fetched %d total events for report.", len(allEvents))

		// --- Data Processing ---
		sort.Slice(allEvents, func(i, j int) bool {
			return allEvents[i].Timestamp.Before(allEvents[j].Timestamp)
		})

		var focusSpans []AppFocusSpan
		var discreteEvents []TimelineEvent
		appTotalTime := make(map[string]time.Duration)
		customEventCounts := make(map[string]int)
		pomoStats := PomodoroSummary{}
		appCategories := make(map[string]string) // AppName -> Y-axis Label
		appCategoryIndex := 0

		var lastFocusEvent *event.Event = nil
		currentFocusApp := "Unknown/Idle" // Track focus for discrete events

		// Determine initial focus state at startTime
		// Look backwards from the first event to find the *last* focus change before startTime
		// (This requires another query or loading slightly more data before startTime)
		// Simpler approach for now: Assume idle or use first event if it's focus change.
		if allEvents[0].Type == event.EventTypeFocusChange {
			currentFocusApp = allEvents[0].AppName
			lastFocusEvent = &allEvents[0] // Treat first event as start of its span
		} else {
			// If first event isn't focus, assume idle from startTime until first focus change
			focusSpans = append(focusSpans, AppFocusSpan{
				AppName:   "Unknown/Idle",
				StartTime: startTime.Format(time.RFC3339),
				// EndTime determined by first focus change
			})
			if _, exists := appCategories["Unknown/Idle"]; !exists {
				appCategories["Unknown/Idle"] = "Unknown/Idle" // Or maybe map to index 0?
			}
		}

		for _, e := range allEvents {
			// Assign app to category map if new
			if e.AppName != "" { // Assign category for any event with app name
				if _, exists := appCategories[e.AppName]; !exists {
					appCategories[e.AppName] = e.AppName // Use AppName as label for now
					appCategoryIndex++
				}
			}

			// --- Process Focus Changes to create Spans ---
			if e.Type == event.EventTypeFocusChange {
				currentFocusApp = e.AppName // Update current focus

				if lastFocusEvent != nil {
					// End the previous span
					span := AppFocusSpan{
						AppName:   lastFocusEvent.AppName,
						StartTime: lastFocusEvent.Timestamp.Format(time.RFC3339),
						EndTime:   e.Timestamp.Format(time.RFC3339),
					}
					if span.AppName == "" {
						span.AppName = "Unknown/Idle"
					} // Handle empty app name
					focusSpans = append(focusSpans, span)

					// Add duration to total time
					if !e.Timestamp.Before(lastFocusEvent.Timestamp) {
						duration := e.Timestamp.Sub(lastFocusEvent.Timestamp)
						appTotalTime[lastFocusEvent.AppName] += duration
					}

				} else if len(focusSpans) > 0 && focusSpans[0].AppName == "Unknown/Idle" && focusSpans[0].EndTime == "" {
					// End the initial idle span if it exists
					focusSpans[0].EndTime = e.Timestamp.Format(time.RFC3339)
					duration := e.Timestamp.Sub(startTime)
					appTotalTime["Unknown/Idle"] += duration
				}
				lastFocusEvent = &e // Current event becomes the start of the next span
			}

			// --- Process Discrete Events ---
			isDiscrete := false
			switch e.Type {
			case event.EventTypePomodoro:
				discreteEvents = append(discreteEvents, TimelineEvent{
					Timestamp: e.Timestamp.Format(time.RFC3339), Type: e.Type, Tag: e.Tag, Notes: e.Notes, FocusApp: currentFocusApp,
				})
				isDiscrete = true
				// Calculate Pomodoro Stats
				state := event.PomodoroState(e.Tag)
				var duration time.Duration
				if e.Value > 0 {
					duration = time.Duration(e.Value) * time.Minute
				} else { // Estimate duration if value is missing (less accurate)
					switch state {
					case event.StateFocus:
						duration = pomoStats.TotalFocusTime / time.Duration(max(1, pomoStats.FocusEvents))
					case event.StateShortBreak:
						duration = pomoStats.TotalBreakTime / time.Duration(max(1, pomoStats.ShortBreakEvents+pomoStats.LongBreakEvents)) // Rough estimate
					case event.StateLongBreak:
						duration = pomoStats.TotalBreakTime / time.Duration(max(1, pomoStats.ShortBreakEvents+pomoStats.LongBreakEvents)) // Rough estimate
					}
				}

				if state == event.StateFocus {
					pomoStats.TotalFocusTime += duration
					pomoStats.FocusEvents++
					pomoStats.TotalCycles = max(pomoStats.TotalCycles, pomoStats.FocusEvents) // Cycle count is number of focus starts
				} else if state == event.StateShortBreak || state == event.StateLongBreak {
					pomoStats.TotalBreakTime += duration
					if state == event.StateShortBreak {
						pomoStats.ShortBreakEvents++
					}
					if state == event.StateLongBreak {
						pomoStats.LongBreakEvents++
					}
				}

			case event.EventTypeCustom:
				discreteEvents = append(discreteEvents, TimelineEvent{
					Timestamp: e.Timestamp.Format(time.RFC3339), Type: e.Type, Tag: e.Tag, Notes: e.Notes, Value: e.Value, FocusApp: currentFocusApp,
				})
				customEventCounts[e.Tag]++
				isDiscrete = true
			case event.EventTypeAppStart, event.EventTypeAppStop:
				discreteEvents = append(discreteEvents, TimelineEvent{
					Timestamp: e.Timestamp.Format(time.RFC3339), Type: e.Type, FocusApp: currentFocusApp,
				})
				isDiscrete = true
			}
			// Add app name to category map even for discrete events if it exists
			if isDiscrete && e.AppName != "" {
				if _, exists := appCategories[e.AppName]; !exists {
					appCategories[e.AppName] = e.AppName
				}
			}
		}

		// Add the final focus span (from last focus change until endTime)
		if lastFocusEvent != nil && endTime.After(lastFocusEvent.Timestamp) {
			span := AppFocusSpan{
				AppName:   lastFocusEvent.AppName,
				StartTime: lastFocusEvent.Timestamp.Format(time.RFC3339),
				EndTime:   endTime.Format(time.RFC3339),
			}
			if span.AppName == "" {
				span.AppName = "Unknown/Idle"
			}
			focusSpans = append(focusSpans, span)
			duration := endTime.Sub(lastFocusEvent.Timestamp)
			appTotalTime[lastFocusEvent.AppName] += duration
		} else if lastFocusEvent == nil && len(allEvents) > 0 {
			// Handle case where there were *no* focus change events in the period
			focusSpans = append(focusSpans, AppFocusSpan{
				AppName:   "Unknown/Idle", // Or determine from non-focus events if possible
				StartTime: startTime.Format(time.RFC3339),
				EndTime:   endTime.Format(time.RFC3339),
			})
			duration := endTime.Sub(startTime)
			appTotalTime["Unknown/Idle"] += duration
			if _, exists := appCategories["Unknown/Idle"]; !exists {
				appCategories["Unknown/Idle"] = "Unknown/Idle"
			}
		}

		// --- Prepare Chart Data ---

		// App Summary (Bar Chart)
		appSummaryChart := BarChartData{Labels: make([]string, 0), Values: make([]float64, 0)}
		// Sort apps by duration descending
		type appDur struct {
			Name string
			Dur  time.Duration
		}
		appDurs := make([]appDur, 0, len(appTotalTime))
		for app, dur := range appTotalTime {
			if app == "" {
				app = "Unknown/Idle"
			} // Group empty app names
			appDurs = append(appDurs, appDur{app, dur})
		}
		sort.Slice(appDurs, func(i, j int) bool {
			return appDurs[i].Dur > appDurs[j].Dur
		})
		for _, ad := range appDurs {
			// Skip entries with zero duration
			if ad.Dur.Minutes() < 0.1 {
				continue
			}
			appSummaryChart.Labels = append(appSummaryChart.Labels, ad.Name)
			appSummaryChart.Values = append(appSummaryChart.Values, ad.Dur.Minutes())
		}

		// Custom Events (Bar Chart)
		customEventChart := BarChartData{Labels: make([]string, 0), Values: make([]float64, 0)}
		// Sort custom events by count descending
		type eventCount struct {
			Tag   string
			Count int
		}
		eventCounts := make([]eventCount, 0, len(customEventCounts))
		for tag, count := range customEventCounts {
			eventCounts = append(eventCounts, eventCount{tag, count})
		}
		sort.Slice(eventCounts, func(i, j int) bool { return eventCounts[i].Count > eventCounts[j].Count })
		for _, ec := range eventCounts {
			customEventChart.Labels = append(customEventChart.Labels, ec.Tag)
			customEventChart.Values = append(customEventChart.Values, float64(ec.Count))
		}

		// Finalize Pomodoro Stats
		pomoStats.FocusTimeStr = formatDurationHuman(pomoStats.TotalFocusTime)
		pomoStats.BreakTimeStr = formatDurationHuman(pomoStats.TotalBreakTime)
		totalPomoActiveTime := pomoStats.TotalFocusTime + pomoStats.TotalBreakTime
		if totalPomoActiveTime > 0 {
			pomoStats.Efficiency = (float64(pomoStats.TotalFocusTime) / float64(totalPomoActiveTime)) * 100.0
		}

		// Marshal data to JSON for embedding
		focusSpansJson, _ := json.Marshal(focusSpans)
		discreteEventsJson, _ := json.Marshal(discreteEvents)
		appSummaryJson, _ := json.Marshal(appSummaryChart)
		customJson, _ := json.Marshal(customEventChart)
		appCategoryJson, _ := json.Marshal(appCategories) // Pass mapping to JS

		// Prepare data for template execution
		reportData := ReportData{
			GeneratedAt:        time.Now().Format("2006-01-02 15:04:05"),
			StartDate:          startTime.Format("2006-01-02"),
			EndDate:            endTime.Format("2006-01-02"),
			FocusSpansJSON:     template.JS(focusSpansJson),
			TimelineEventsJSON: template.JS(discreteEventsJson),
			AppSummaryJSON:     template.JS(appSummaryJson),
			CustomEventsJSON:   template.JS(customJson),
			PomodoroStats:      pomoStats,
			AppCategoryMap:     appCategories,                // Pass raw map for potential Go-side use
			AppCategoryJSON:    template.JS(appCategoryJson), // Pass JSON map for JS use
		}

		// --- Template Parsing and Execution ---
		exePath, err := os.Executable()
		if err != nil {
			log.Printf("Warning: Cannot determine executable path: %v...", err)
			exePath = "."
		}
		templatePath := filepath.Join(filepath.Dir(exePath), "template.html")
		if _, err := os.Stat(templatePath); os.IsNotExist(err) {
			templatePath = "template.html"
			if _, err := os.Stat(templatePath); os.IsNotExist(err) {
				log.Fatalf("Error: template.html not found near executable or in current directory.")
			}
		}

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

func formatDurationHuman(d time.Duration) string {
	d = d.Round(time.Minute) // Round to nearest minute for summary
	h := d / time.Hour
	d -= h * time.Hour
	m := d / time.Minute

	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
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
