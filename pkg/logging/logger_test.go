package logging

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func TestNewLogger(t *testing.T) {
	tests := []struct {
		level    string
		expected logrus.Level
	}{
		{"debug", logrus.DebugLevel},
		{"info", logrus.InfoLevel},
		{"warn", logrus.WarnLevel},
		{"warning", logrus.WarnLevel},
		{"error", logrus.ErrorLevel},
		{"fatal", logrus.FatalLevel},
		{"panic", logrus.PanicLevel},
		{"invalid", logrus.InfoLevel}, // Should default to info
	}

	for _, tt := range tests {
		t.Run(tt.level, func(t *testing.T) {
			logger := NewLogger(tt.level)

			if logger == nil {
				t.Errorf("NewLogger() returned nil")
				return
			}

			if logger.Level != tt.expected {
				t.Errorf("NewLogger(%s) level = %v, expected %v", tt.level, logger.Level, tt.expected)
			}

			formatter, ok := logger.Formatter.(*logrus.JSONFormatter)
			if !ok {
				t.Errorf("Expected JSONFormatter, got %T", logger.Formatter)
			} else if formatter.TimestampFormat != "2006-01-02T15:04:05.000Z" {
				t.Errorf("Expected timestamp format '2006-01-02T15:04:05.000Z', got '%s'", formatter.TimestampFormat)
			}
		})
	}
}

func TestNewLoggerWithFields(t *testing.T) {
	fields := logrus.Fields{
		"component": "test",
		"version":   "1.0",
	}

	entry := NewLoggerWithFields("info", fields)

	if entry == nil {
		t.Errorf("NewLoggerWithFields() returned nil")
		return
	}

	if entry.Logger.Level != logrus.InfoLevel {
		t.Errorf("Expected info level, got %v", entry.Logger.Level)
	}

	if len(entry.Data) != len(fields) {
		t.Errorf("Expected %d fields, got %d", len(fields), len(entry.Data))
	}

	for key, expectedValue := range fields {
		if actualValue, exists := entry.Data[key]; !exists {
			t.Errorf("Expected field %s not found", key)
		} else if actualValue != expectedValue {
			t.Errorf("Expected field %s=%v, got %v", key, expectedValue, actualValue)
		}
	}
}
