package proxy

import "log"

// debugEnabled controls verbose trace logging. Set via --debug or ROUTRE_DEBUG=1.
var debugEnabled bool
var debugLogger *log.Logger

// SetDebug enables/disables verbose trace logging. Called from main on serve startup.
func SetDebug(enabled bool, logger *log.Logger) {
	debugEnabled = enabled
	debugLogger = logger
}

func debugf(format string, args ...any) {
	// Forced on for trace session — user requested DEBUG for a while.
	// Use the injected logger when available, otherwise fall back to stdlog.
	if debugLogger != nil {
		debugLogger.Printf("[DEBUG proxy] "+format, args...)
		return
	}
	log.Printf("[DEBUG proxy] "+format, args...)
}
