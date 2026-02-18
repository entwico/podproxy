package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/mattn/go-colorable"
	slogzap "github.com/samber/slog-zap/v2"
	"github.com/xlab/closer"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var Logger *slog.Logger

func SetupGlobalLogger(c *Config) error {
	logConfig := c.Log
	newLogEncoder := func(f string, c zapcore.EncoderConfig) zapcore.Encoder {
		if f == "json" {
			return zapcore.NewJSONEncoder(c)
		}

		return zapcore.NewConsoleEncoder(c)
	}

	zapConfig := zap.NewProductionConfig()
	zapConfig.DisableCaller = true
	encoderConfig := zapConfig.EncoderConfig

	encoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	if !logConfig.Timestamp {
		encoderConfig.TimeKey = ""
		encoderConfig.EncodeTime = nil
	}

	logFormatter := strings.ToLower(logConfig.Formatter)
	consoleWriter := zapcore.AddSync(os.Stdout)

	if logConfig.Colors && logFormatter != "json" {
		encoderConfig.EncodeLevel = zapcore.LowercaseColorLevelEncoder
		consoleWriter = zapcore.AddSync(colorable.NewColorableStdout())
	}

	logLevel, err := zapcore.ParseLevel(logConfig.Level)
	if err != nil {
		return err
	}

	logCores := []zapcore.Core{zapcore.NewCore(newLogEncoder(logFormatter, encoderConfig), consoleWriter, logLevel)}

	if logConfig.File != "" {
		logFile, err := os.OpenFile(logConfig.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}

		closer.Bind(func() {
			_ = logFile.Close()
		})

		fileWriter := zapcore.AddSync(logFile)
		encoderConfig.EncodeLevel = zapcore.LowercaseLevelEncoder
		logCores = append(logCores, zapcore.NewCore(newLogEncoder(logFormatter, encoderConfig), fileWriter, logLevel))
	}

	zapLogger := zap.New(zapcore.NewTee(logCores...), zap.AddStacktrace(zapcore.PanicLevel))

	closer.Bind(func() {
		_ = zapLogger.Sync()
	})

	Logger = slog.New(slogzap.Option{
		Level:  slog.LevelDebug,
		Logger: zapLogger,
		// slog-common's ReplaceError expands errors into map[string]any with
		// "error", "kind", and "stack" keys. "kind" is just the Go type name
		// and "stack" is always nil — flatten back to a plain string.
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			m, ok := a.Value.Any().(map[string]any)
			if !ok {
				return a
			}

			if errMsg, hasErr := m["error"]; hasErr {
				if _, hasKind := m["kind"]; hasKind {
					return slog.String(a.Key, fmt.Sprint(errMsg))
				}
			}

			return a
		},
	}.NewZapHandler())
	slog.SetDefault(Logger)

	return nil
}
