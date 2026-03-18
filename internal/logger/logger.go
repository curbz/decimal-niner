package logger

import (
	"os"

	"github.com/sirupsen/logrus"
)

// Log is our exported global logger instance
var Log = logrus.New()

// Setup initializes the logger settings
func Init(level string) {
	// 1. Open the log file (Append mode)
	file, err := os.OpenFile("d9log.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0666)
	if err != nil {
		logrus.Errorf("Failed to open log file: %v", err)
		return
	}

	// 2. Configure the File Output (Plain text, no colors)
	Log.SetOutput(file)
	Log.SetFormatter(&logrus.TextFormatter{
		DisableColors: true,
		FullTimestamp: true,
	})

	// 3. Add the Console Hook (Colored output for VSCode)
	Log.AddHook(&ConsoleHook{})

	levelInt, err := logrus.ParseLevel(level)
	if err != nil {
		// If the string is invalid fall back to Info
		level = "info"
		levelInt = logrus.InfoLevel
	}

	Log.SetLevel(levelInt)
	Log.Info("logging level set to ", level)
}

// ConsoleHook sends logs to Stdout with colors enabled
type ConsoleHook struct{}

func (h *ConsoleHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

func (h *ConsoleHook) Fire(entry *logrus.Entry) error {
	// Use a separate formatter specifically for the console
	colorFormatter := &logrus.TextFormatter{
		ForceColors:   true,
		FullTimestamp: true,
	}

	line, err := colorFormatter.Format(entry)
	if err != nil {
		return err
	}

	_, err = os.Stdout.Write(line)
	return err
}
