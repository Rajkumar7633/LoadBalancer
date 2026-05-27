package logger

import (
	"fmt"
	"log"
	"os"
	"time"
)

type Logger struct {
	level      string
	structured bool
	logger     *log.Logger
}

func NewLogger() *Logger {
	return &Logger{
		level:      "info",
		structured: true,
		logger:     log.New(os.Stdout, "", 0),
	}
}

func (l *Logger) Info(msg string, keyvals ...interface{}) {
	l.log("INFO", msg, keyvals...)
}

func (l *Logger) Error(msg string, keyvals ...interface{}) {
	l.log("ERROR", msg, keyvals...)
}

func (l *Logger) Debug(msg string, keyvals ...interface{}) {
	l.log("DEBUG", msg, keyvals...)
}

func (l *Logger) Warn(msg string, keyvals ...interface{}) {
	l.log("WARN", msg, keyvals...)
}

func (l *Logger) Fatal(msg string, keyvals ...interface{}) {
	l.log("FATAL", msg, keyvals...)
	os.Exit(1)
}

func (l *Logger) log(level, msg string, keyvals ...interface{}) {
	timestamp := time.Now().UTC().Format(time.RFC3339)

	if l.structured {
		// Structured JSON logging
		logEntry := fmt.Sprintf(`{"timestamp":"%s","level":"%s","message":"%s"`, timestamp, level, msg)

		// Add key-value pairs
		for i := 0; i < len(keyvals); i += 2 {
			if i+1 < len(keyvals) {
				logEntry += fmt.Sprintf(`,"%s":"%v"`, keyvals[i], keyvals[i+1])
			}
		}

		logEntry += "}"
		l.logger.Println(logEntry)
	} else {
		// Traditional logging
		logMsg := fmt.Sprintf("[%s] %s: %s", timestamp, level, msg)
		if len(keyvals) > 0 {
			logMsg += " |"
			for i := 0; i < len(keyvals); i += 2 {
				if i+1 < len(keyvals) {
					logMsg += fmt.Sprintf(" %s=%v", keyvals[i], keyvals[i+1])
				}
			}
		}
		l.logger.Println(logMsg)
	}
}
