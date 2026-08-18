package podproxy

import (
	"fmt"
	"os"
)

type logLevel int

const (
	levelError logLevel = iota
	levelInfo
	levelDebug
)

func parseLogLevel(value string) (logLevel, bool) {
	switch value {
	case "", "error":
		return levelError, true
	case "info":
		return levelInfo, true
	case "debug":
		return levelDebug, true
	}

	return levelError, false
}

type logger struct {
	level logLevel
}

func (l *logger) errorf(format string, args ...any) {
	l.writef(levelError, format, args...)
}

func (l *logger) infof(format string, args ...any) {
	l.writef(levelInfo, format, args...)
}

func (l *logger) debugf(format string, args ...any) {
	l.writef(levelDebug, format, args...)
}

func (l *logger) writef(level logLevel, format string, args ...any) {
	if level > l.level {
		return
	}

	fmt.Fprintf(os.Stderr, "[podproxy] "+format+"\n", args...)
}
