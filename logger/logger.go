// Package logger provides a structured logging interface for MALT.
// It wraps go.uber.org/zap for high-performance logging with support
// for performance analysis and debugging.
package logger

import (
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger is the global logger instance.
var Logger *zap.SugaredLogger

// zapLogger holds the underlying zap logger for the global Logger instance.
// It is used to properly close the logger and its file handles.
var zapLogger *zap.Logger

// openFiles stores references to all file handles opened by the logger.
// This allows us to properly close them in Close().
var openFiles []io.Closer

// atomicLevel holds the current log level and allows dynamic changes.
var atomicLevel zap.AtomicLevel

// Level represents log severity level.
type Level = zapcore.Level

// Log levels.
var (
	DebugLevel = zapcore.DebugLevel
	InfoLevel  = zapcore.InfoLevel
	WarnLevel  = zapcore.WarnLevel
	ErrorLevel = zapcore.ErrorLevel
)

// Field represents a structured log field.
type Field = zap.Field

// Common field constructors.
var (
	String  = zap.String
	Int     = zap.Int
	Int64   = zap.Int64
	Float64 = zap.Float64
	Bool    = zap.Bool
	Err     = zap.Error // Use Err for zap.Error field
	Any     = zap.Any
)

// Config holds logger configuration.
type Config struct {
	// Level is the minimum log level.
	Level Level

	// Development mode uses human-friendly output.
	Development bool

	// OutputPaths are paths to write logs to.
	// Defaults to ["stdout"].
	OutputPaths []string

	// ErrorOutputPaths are paths to write internal errors to.
	// Defaults to ["stderr"].
	ErrorOutputPaths []string
}

// DefaultConfig returns the default logger configuration.
func DefaultConfig() Config {
	return Config{
		Level:            InfoLevel,
		Development:      false,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}
}

// DevelopmentConfig returns a development-friendly configuration.
func DevelopmentConfig() Config {
	return Config{
		Level:            DebugLevel,
		Development:      true,
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}
}

// Init initializes the global logger with the given config.
func Init(cfg Config) error {
	// Close the previous logger and its file handles
	if zapLogger != nil {
		_ = zapLogger.Sync()
	}
	closeOpenFiles()

	encoderConfig := zap.NewProductionEncoderConfig()
	if cfg.Development {
		encoderConfig = zap.NewDevelopmentEncoderConfig()
		encoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	}

	encoder := zapcore.NewJSONEncoder(encoderConfig)
	if cfg.Development {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	}

	// Create write syncers for output paths
	var writeSyncers []zapcore.WriteSyncer
	for _, path := range cfg.OutputPaths {
		if path == "stdout" {
			writeSyncers = append(writeSyncers, zapcore.AddSync(os.Stdout))
		} else {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			openFiles = append(openFiles, file)
			writeSyncers = append(writeSyncers, zapcore.AddSync(file))
		}
	}

	// Create error write syncers
	var errorSyncers []zapcore.WriteSyncer
	for _, path := range cfg.ErrorOutputPaths {
		if path == "stderr" {
			errorSyncers = append(errorSyncers, zapcore.AddSync(os.Stderr))
		} else {
			file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
			if err != nil {
				return err
			}
			openFiles = append(openFiles, file)
			errorSyncers = append(errorSyncers, zapcore.AddSync(file))
		}
	}

	atomicLevel = zap.NewAtomicLevelAt(cfg.Level)

	core := zapcore.NewCore(
		encoder,
		zapcore.NewMultiWriteSyncer(writeSyncers...),
		atomicLevel,
	)

	// Add error core if specified
	if len(errorSyncers) > 0 {
		errorCore := zapcore.NewCore(
			encoder,
			zapcore.NewMultiWriteSyncer(errorSyncers...),
			zapcore.ErrorLevel,
		)
		core = zapcore.NewTee(core, errorCore)
	}

	zapLogger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	Logger = zapLogger.Sugar()

	return nil
}

// InitDevelopment initializes a development logger.
func InitDevelopment() error {
	return Init(DevelopmentConfig())
}

// InitProduction initializes a production logger.
func InitProduction() error {
	return Init(DefaultConfig())
}

// SetLevel changes the log level dynamically.
func SetLevel(level Level) {
	atomicLevel.SetLevel(level)
}

// Debug logs a debug message.
func Debug(msg string, fields ...Field) {
	if Logger != nil {
		Logger.Debugw(msg, toAnySlice(fields)...)
	}
}

// Info logs an info message.
func Info(msg string, fields ...Field) {
	if Logger != nil {
		Logger.Infow(msg, toAnySlice(fields)...)
	}
}

// Warn logs a warning message.
func Warn(msg string, fields ...Field) {
	if Logger != nil {
		Logger.Warnw(msg, toAnySlice(fields)...)
	}
}

// Error logs an error message.
func Error(msg string, fields ...Field) {
	if Logger != nil {
		Logger.Errorw(msg, toAnySlice(fields)...)
	}
}

// Fatal logs a fatal message and exits.
func Fatal(msg string, fields ...Field) {
	if Logger != nil {
		Logger.Fatalw(msg, toAnySlice(fields)...)
	}
}

// With returns a logger with additional fields.
func With(fields ...Field) *zap.SugaredLogger {
	if Logger != nil {
		return Logger.With(toAnySlice(fields)...)
	}
	return nil
}

// Named returns a logger with a name prefix.
func Named(name string) *zap.SugaredLogger {
	if Logger != nil {
		return Logger.Named(name)
	}
	return nil
}

// Sync flushes any buffered logs.
func Sync() error {
	if Logger != nil {
		return Logger.Sync()
	}
	return nil
}

// Close closes the logger and releases any file handles.
// This should be called when the logger is no longer needed, especially important
// for tests using temporary directories to avoid file locking issues on Windows.
func Close() error {
	if zapLogger != nil {
		_ = zapLogger.Sync()
	}
	closeOpenFiles()
	return nil
}

// closeOpenFiles closes all open file handles.
func closeOpenFiles() {
	for _, f := range openFiles {
		_ = f.Close()
	}
	openFiles = []io.Closer{}
}

// toAnySlice converts Field slice to Any slice for SugaredLogger.
func toAnySlice(fields []Field) []any {
	result := make([]any, len(fields))
	for i, f := range fields {
		result[i] = f
	}
	return result
}
