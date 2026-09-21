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
	// Honour the debug flag: this runs on every request, so an unconditional
	// log call is per-request stderr I/O on the hot path.
	if !debugEnabled {
		return
	}
	if debugLogger != nil {
		debugLogger.Printf("[DEBUG proxy] "+format, args...)
		return
	}
	log.Printf("[DEBUG proxy] "+format, args...)
}
