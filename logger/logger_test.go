package logger

import (
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/zap/zapcore"
)

func TestInit_Success(t *testing.T) {
	cfg := DefaultConfig()

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() returned unexpected error: %v", err)
	}

	if Logger == nil {
		t.Fatal("Init() did not initialize the global Logger")
	}
}

func TestInitDevelopment(t *testing.T) {
	err := InitDevelopment()
	if err != nil {
		t.Fatalf("InitDevelopment() returned unexpected error: %v", err)
	}

	if Logger == nil {
		t.Fatal("InitDevelopment() did not initialize the global Logger")
	}
}

func TestInitProduction(t *testing.T) {
	err := InitProduction()
	if err != nil {
		t.Fatalf("InitProduction() returned unexpected error: %v", err)
	}

	if Logger == nil {
		t.Fatal("InitProduction() did not initialize the global Logger")
	}
}

func TestInit_InvalidOutputPath(t *testing.T) {
	cfg := Config{
		Level:           InfoLevel,
		Development:     false,
		OutputPaths:     []string{"/nonexistent/path/that/does/not/exist/logfile.log"},
		ErrorOutputPaths: []string{"stderr"},
	}

	err := Init(cfg)
	if err == nil {
		t.Fatal("Init() with invalid output path should have returned an error")
	}
}

func TestSetLevel(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Level = InfoLevel

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() returned unexpected error: %v", err)
	}

	// SetLevel should not panic for any valid level
	SetLevel(DebugLevel)
	SetLevel(WarnLevel)
	SetLevel(ErrorLevel)
	SetLevel(InfoLevel)

	// Verify the logger is still functional after level changes
	if Logger == nil {
		t.Fatal("Logger became nil after SetLevel() calls")
	}

	// SetLevel to DebugLevel and verify debug logging is enabled
	SetLevel(DebugLevel)

	// SetLevel to ErrorLevel and verify the logger is still functional
	SetLevel(ErrorLevel)

	// Reset to InfoLevel for other tests
	SetLevel(InfoLevel)
}

func TestLogger_WithFields(t *testing.T) {
	err := InitDevelopment()
	if err != nil {
		t.Fatalf("InitDevelopment() returned unexpected error: %v", err)
	}

	logged := With(
		String("key", "value"),
		Int("count", 42),
	)

	if logged == nil {
		t.Fatal("With() returned nil logger")
	}
}

func TestLogger_Named(t *testing.T) {
	err := InitDevelopment()
	if err != nil {
		t.Fatalf("InitDevelopment() returned unexpected error: %v", err)
	}

	named := Named("test-component")

	if named == nil {
		t.Fatal("Named() returned nil logger")
	}
}

func TestSync(t *testing.T) {
	// Use a config without stderr to avoid platform-specific sync errors
	// on /dev/stderr (macOS returns "bad file descriptor").
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "sync-test.log")
	errFile := filepath.Join(tmpDir, "sync-error.log")

	cfg := Config{
		Level:           DebugLevel,
		Development:     true,
		OutputPaths:     []string{logFile},
		ErrorOutputPaths: []string{errFile},
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() returned unexpected error: %v", err)
	}

	err = Sync()
	if err != nil {
		t.Fatalf("Sync() returned unexpected error: %v", err)
	}
}

func TestInit_MultipleOutputPaths(t *testing.T) {
	cfg := Config{
		Level:           DebugLevel,
		Development:     true,
		OutputPaths:     []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() with stdout only returned unexpected error: %v", err)
	}

	if Logger == nil {
		t.Fatal("Init() with stdout only did not initialize the global Logger")
	}
}

func TestInit_FileOutput(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "test.log")
	errFile := filepath.Join(tmpDir, "error.log")

	cfg := Config{
		Level:           DebugLevel,
		Development:     false,
		OutputPaths:     []string{logFile},
		ErrorOutputPaths: []string{errFile},
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() with file output returned unexpected error: %v", err)
	}

	if Logger == nil {
		t.Fatal("Init() with file output did not initialize the global Logger")
	}

	// Verify the file was created
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		t.Fatal("Log file was not created")
	}

	// Write a log entry and verify it appears in the file
	Info("test message", String("test", "value"))

	err = Sync()
	if err != nil {
		t.Fatalf("Sync() returned unexpected error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Log file is empty after writing")
	}
}

func TestLogLevel_Filtering(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "filter-test.log")
	errFile := filepath.Join(tmpDir, "filter-error.log")

	cfg := Config{
		Level:           WarnLevel,
		Development:     false,
		OutputPaths:     []string{logFile},
		ErrorOutputPaths: []string{errFile},
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() returned unexpected error: %v", err)
	}

	// Log at all levels
	Debug("debug message")
	Info("info message")
	Warn("warn message")
	Error("error message")

	err = Sync()
	if err != nil {
		t.Fatalf("Sync() returned unexpected error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	content := string(data)

	// Debug and Info should be filtered out
	if contains(content, "debug message") {
		t.Error("Debug message should have been filtered out at WarnLevel")
	}
	if contains(content, "info message") {
		t.Error("Info message should have been filtered out at WarnLevel")
	}

	// Warn and Error should be present
	if !contains(content, "warn message") {
		t.Error("Warn message should be present at WarnLevel")
	}
	if !contains(content, "error message") {
		t.Error("Error message should be present at WarnLevel")
	}
}

// contains checks if substr is in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && s != "" && findSubstring(s, substr)
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestSetLevel_DynamicChange(t *testing.T) {
	tmpDir := t.TempDir()
	logFile := filepath.Join(tmpDir, "dynamic-level-test.log")
	errFile := filepath.Join(tmpDir, "dynamic-error.log")

	// Start with ErrorLevel
	cfg := Config{
		Level:           ErrorLevel,
		Development:     false,
		OutputPaths:     []string{logFile},
		ErrorOutputPaths: []string{errFile},
	}

	err := Init(cfg)
	if err != nil {
		t.Fatalf("Init() returned unexpected error: %v", err)
	}

	// Warn should be filtered at ErrorLevel
	Warn("before level change")

	// Dynamically change to DebugLevel
	SetLevel(DebugLevel)

	// Now Warn and Info should pass through
	Warn("after level change to debug")
	Info("info after level change")
	Error("error after level change")

	err = Sync()
	if err != nil {
		t.Fatalf("Sync() returned unexpected error: %v", err)
	}

	data, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("Failed to read log file: %v", err)
	}

	content := string(data)

	if contains(content, "before level change") {
		t.Error("Warn message should have been filtered out at ErrorLevel")
	}
	if !contains(content, "after level change to debug") {
		t.Error("Warn message should appear after SetLevel(DebugLevel)")
	}
	if !contains(content, "info after level change") {
		t.Error("Info message should appear after SetLevel(DebugLevel)")
	}
}

func TestWith_NilWhenNotInitialized(t *testing.T) {
	// Save current logger
	savedLogger := Logger

	// Set Logger to nil
	Logger = nil

	result := With(String("key", "value"))
	if result != nil {
		t.Error("With() should return nil when Logger is nil")
	}

	// Restore
	Logger = savedLogger
}

func TestNamed_NilWhenNotInitialized(t *testing.T) {
	// Save current logger
	savedLogger := Logger

	// Set Logger to nil
	Logger = nil

	result := Named("test")
	if result != nil {
		t.Error("Named() should return nil when Logger is nil")
	}

	// Restore
	Logger = savedLogger
}

func TestSync_NilLogger(t *testing.T) {
	// Save current logger
	savedLogger := Logger

	// Set Logger to nil
	Logger = nil

	err := Sync()
	if err != nil {
		t.Errorf("Sync() should return nil when Logger is nil, got: %v", err)
	}

	// Restore
	Logger = savedLogger
}

func TestInit_CustomEncoderConfig(t *testing.T) {
	// Test that development mode uses console encoder with color
	err := Init(Config{
		Level:           DebugLevel,
		Development:     true,
		OutputPaths:     []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	})
	if err != nil {
		t.Fatalf("Init() in development mode returned error: %v", err)
	}

	if Logger == nil {
		t.Fatal("Logger is nil after development Init")
	}

	// Test that production mode uses JSON encoder
	err = Init(Config{
		Level:           InfoLevel,
		Development:     false,
		OutputPaths:     []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	})
	if err != nil {
		t.Fatalf("Init() in production mode returned error: %v", err)
	}

	if Logger == nil {
		t.Fatal("Logger is nil after production Init")
	}
}

func TestLogLevels_Constants(t *testing.T) {
	// Verify that the exported level constants match zapcore levels
	if DebugLevel != zapcore.DebugLevel {
		t.Errorf("DebugLevel = %v, want %v", DebugLevel, zapcore.DebugLevel)
	}
	if InfoLevel != zapcore.InfoLevel {
		t.Errorf("InfoLevel = %v, want %v", InfoLevel, zapcore.InfoLevel)
	}
	if WarnLevel != zapcore.WarnLevel {
		t.Errorf("WarnLevel = %v, want %v", WarnLevel, zapcore.WarnLevel)
	}
	if ErrorLevel != zapcore.ErrorLevel {
		t.Errorf("ErrorLevel = %v, want %v", ErrorLevel, zapcore.ErrorLevel)
	}
}
