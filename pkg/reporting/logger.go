package reporting

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// LogLevel represents the logging level
type LogLevel string

const (
	LogLevelDebug LogLevel = "debug"
	LogLevelInfo  LogLevel = "info"
	LogLevelWarn  LogLevel = "warn"
	LogLevelError LogLevel = "error"
)

// LogFormat represents the logging format
type LogFormat string

const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// LoggerConfig contains logger configuration
type LoggerConfig struct {
	Level  LogLevel
	Format LogFormat
	Output io.Writer
}

// Logger provides structured logging
type Logger struct {
	logger zerolog.Logger
}

// NewLogger creates a new structured logger
func NewLogger(cfg LoggerConfig) *Logger {
	// Set default output if not specified
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}

	// Configure output format
	var output io.Writer = cfg.Output
	if cfg.Format == LogFormatText {
		output = zerolog.ConsoleWriter{
			Out:        cfg.Output,
			TimeFormat: time.RFC3339,
			NoColor:    false,
		}
	}

	// Create logger
	zlog := zerolog.New(output).With().Timestamp().Logger()

	// Set log level
	switch cfg.Level {
	case LogLevelDebug:
		zlog = zlog.Level(zerolog.DebugLevel)
	case LogLevelInfo:
		zlog = zlog.Level(zerolog.InfoLevel)
	case LogLevelWarn:
		zlog = zlog.Level(zerolog.WarnLevel)
	case LogLevelError:
		zlog = zlog.Level(zerolog.ErrorLevel)
	default:
		zlog = zlog.Level(zerolog.InfoLevel)
	}

	return &Logger{logger: zlog}
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, fields ...interface{}) {
	event := l.logger.Debug()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Info logs an info message
func (l *Logger) Info(msg string, fields ...interface{}) {
	event := l.logger.Info()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, fields ...interface{}) {
	event := l.logger.Warn()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Error logs an error message
func (l *Logger) Error(msg string, fields ...interface{}) {
	event := l.logger.Error()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(msg string, fields ...interface{}) {
	event := l.logger.Fatal()
	l.addFields(event, fields...)
	event.Msg(msg)
}

// WithField creates a child logger with an additional field
func (l *Logger) WithField(key string, value interface{}) *Logger {
	return &Logger{
		logger: l.logger.With().Interface(key, value).Logger(),
	}
}

// WithFields creates a child logger with additional fields
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	ctx := l.logger.With()
	for k, v := range fields {
		ctx = ctx.Interface(k, v)
	}
	return &Logger{
		logger: ctx.Logger(),
	}
}

// addFields adds key-value pairs to a log event
func (l *Logger) addFields(event *zerolog.Event, fields ...interface{}) {
	if len(fields)%2 != 0 {
		event.Str("error", "odd number of fields")
		return
	}

	for i := 0; i < len(fields); i += 2 {
		key, ok := fields[i].(string)
		if !ok {
			event.Str("error", fmt.Sprintf("field key at index %d is not a string", i))
			continue
		}

		value := fields[i+1]
		event.Interface(key, value)
	}
}

// GetZerologLogger returns the underlying zerolog logger
func (l *Logger) GetZerologLogger() zerolog.Logger {
	return l.logger
}

// InitGlobalLogger initializes the global logger
func InitGlobalLogger(cfg LoggerConfig) {
	// Set default output if not specified
	if cfg.Output == nil {
		cfg.Output = os.Stdout
	}

	// Configure output format
	var output io.Writer = cfg.Output
	if cfg.Format == LogFormatText {
		output = zerolog.ConsoleWriter{
			Out:        cfg.Output,
			TimeFormat: time.RFC3339,
			NoColor:    false,
		}
	}

	// Set global logger
	log.Logger = zerolog.New(output).With().Timestamp().Logger()

	// Set log level
	switch cfg.Level {
	case LogLevelDebug:
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case LogLevelInfo:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	case LogLevelWarn:
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case LogLevelError:
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
}

// Global convenience functions for zerolog

// Debug logs a debug message using the global logger
func Debug(msg string) {
	log.Debug().Msg(msg)
}

// Info logs an info message using the global logger
func Info(msg string) {
	log.Info().Msg(msg)
}

// Warn logs a warning message using the global logger
func Warn(msg string) {
	log.Warn().Msg(msg)
}

// Error logs an error message using the global logger
func Error(msg string) {
	log.Error().Msg(msg)
}

// Fatal logs a fatal message and exits using the global logger
func Fatal(msg string) {
	log.Fatal().Msg(msg)
}
