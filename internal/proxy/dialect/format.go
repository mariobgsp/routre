package dialect

import (
	"bytes"
	"encoding/json"
	"strings"
)

type Format int

const (
	FormatUnknown Format = iota
	FormatOpenAI
	FormatAnthropic
	FormatResponses
	FormatGemini
)

func (f Format) String() string {
	switch f {
	case FormatOpenAI:
		return "openai"
	case FormatAnthropic:
		return "anthropic"
	case FormatResponses:
		return "responses"
	case FormatGemini:
		return "gemini"
	default:
		return "unknown"
	}
}

type Pair struct {
	From, To Format
}

func DetectFormat(path string, body []byte) Format {
	if strings.HasSuffix(path, "/messages") {
		return FormatAnthropic
	}
	if strings.HasSuffix(path, "/responses") {
		return FormatResponses
	}
	if strings.HasSuffix(path, "/chat/completions") {
		return FormatOpenAI
	}
	if bytes.Contains(body, []byte(`"max_tokens"`)) && !bytes.Contains(body, []byte(`"stream_options"`)) {
		return FormatAnthropic
	}
	return FormatOpenAI
}

func IsStreaming(body []byte) bool {
	var probe struct {
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return false
	}
	return probe.Stream
}

func KindToFormat(kind string) Format {
	switch kind {
	case "anthropic":
		return FormatAnthropic
	case "gemini":
		return FormatGemini
	case "responses":
		return FormatResponses
	default:
		return FormatOpenAI
	}
}
