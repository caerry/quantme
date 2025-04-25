package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"quantme/internal/app"
	"quantme/internal/config"
)

var (
	// Define command-line flags
	configPath = flag.String("c", "", "Path to configuration file (e.g., config.yaml). Defaults to ./config.yaml, ~/.config/quantme/config.yaml, /etc/quantme/config.yaml")
	logPath    = flag.String("log", "", "Path to log file (optional, defaults to stderr)")
	// daemonize  = flag.Bool("d", false, "Run in daemon mode (Requires uncommenting daemon code)")
)

// setupLogging configures the log output destination.
func setupLogging(logFilePath string) (*os.File, error) {

	if logFilePath == "" {
		log.SetOutput(os.Stderr) // Default: log to standard error
		log.Println("Logging to stderr")
		return nil, nil
	}

	// Ensure the directory for the log file exists
	dir := filepath.Dir(logFilePath)
	if err := os.MkdirAll(dir, 0750); err != nil { // Use 0750 for permissions
		return nil, fmt.Errorf("failed to create log directory %s: %w", dir, err)
	}

	// Open the log file for appending, create if it doesn't exist
	file, err := os.OpenFile(logFilePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open log file %s: %w", logFilePath, err)
	}

	log.SetOutput(file)                                              // Set log output to the file
	log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile) // Add microsecond and file/line info
	log.Printf("Logging to file: %s", logFilePath)
	return file, nil
}

func main() {

	// Parse the command-line flags provided by the user
	flag.Parse()

	// Set up logging based on the -log flag
	logFile, logErr := setupLogging(*logPath)
	if logErr != nil {
		// If file logging fails, log the error to stderr and continue logging to stderr
		fmt.Fprintf(os.Stderr, "Error setting up file logging: %v. Logging to stderr instead.\n", logErr)
		log.SetOutput(os.Stderr)
		log.SetFlags(log.LstdFlags | log.Lmicroseconds | log.Lshortfile) // Ensure flags are set for stderr too
	}
	// If a log file was successfully opened, ensure it's closed upon exit
	if logFile != nil {
		defer logFile.Close()
	}

	// --- Optional Daemonization Placeholder ---
	// if *daemonize {
	//     // ... Daemonization code using go-daemon would go here ...
	//     // Remember to handle PID files, working directory, etc.
	//     // Make sure logging is configured *before* daemonizing if logging to a file.
	//     log.Println("Daemon mode requested (implementation commented out)")
	// }
	// -----------------------------------------

	// Load the application configuration
	// Uses viper which checks flags, env vars, and config files (./, ~/.config/quantme/, /etc/quantme/)
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		log.Fatalf("FATAL: Failed to load configuration: %v", err)
	}

	// Create the main application instance
	application, err := app.NewApp(cfg)
	if err != nil {
		log.Fatalf("FATAL: Failed to create application: %v", err)
	}

	// Run the application. This will block until the app exits (e.g., via Ctrl+C).
	if err := application.Run(); err != nil {
		// Log the error that caused the application to exit abnormally
		log.Fatalf("FATAL: Application exited with error: %v", err)
	}

	// Application exited gracefully
	log.Println("QuantMe finished successfully.")
}
