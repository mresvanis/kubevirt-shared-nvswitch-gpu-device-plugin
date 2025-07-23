package logging

import (
	"os"

	"github.com/sirupsen/logrus"
)

func NewLogger(level string) *logrus.Logger {
	logger := logrus.New()
	logger.SetOutput(os.Stdout)
	logger.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: "2006-01-02T15:04:05.000Z",
	})

	logLevel, err := logrus.ParseLevel(level)
	if err != nil {
		logLevel = logrus.InfoLevel
	}
	logger.SetLevel(logLevel)

	return logger
}

func NewLoggerWithFields(level string, fields logrus.Fields) *logrus.Entry {
	logger := NewLogger(level)
	return logger.WithFields(fields)
}
