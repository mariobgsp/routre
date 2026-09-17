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
	if f >= FormatUnknown && f <= FormatGemini {
		return [...]string{"unknown", "openai", "anthropic", "responses", "gemini"}[f]
	}
	return "unknown"
}

type Pair struct {
	From, To Format
}

func DetectFormat(path string, body []byte) Format {
	for _, p := range []struct {
		suffix string
		f      Format
	}{
		{"/messages", FormatAnthropic},
		{"/responses", FormatResponses},
		{"/chat/completions", FormatOpenAI},
	} {
		if strings.HasSuffix(path, p.suffix) {
			return p.f
		}
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
	if f, ok := map[string]Format{
		"anthropic": FormatAnthropic,
		"gemini":    FormatGemini,
		"responses": FormatResponses,
	}[kind]; ok {
		return f
	}
	return FormatOpenAI
}
