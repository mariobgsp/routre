package router

import "log"

var debugEnabled bool
var debugLogger *log.Logger

func SetDebug(enabled bool, logger *log.Logger) {
	debugEnabled = enabled
	debugLogger = logger
}

func debugf(format string, args ...any) {
	if debugLogger != nil {
		debugLogger.Printf("[DEBUG router] "+format, args...)
		return
	}
	if debugEnabled {
		log.Printf("[DEBUG router] "+format, args...)
	}
}

var _ = debugf
