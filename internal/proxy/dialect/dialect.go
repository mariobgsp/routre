package dialect

import (
	"errors"
	"fmt"
	"io"
)

var ErrUnsupported = errors.New("unsupported dialect pair")

type Dialect struct{}

func New() *Dialect { return &Dialect{} }

func (d *Dialect) Request(from, to Format, body []byte) ([]byte, error) {
	switch {
	case from == FormatOpenAI && to == FormatAnthropic:
		return openAItoAnthropic(body)
	case from == FormatAnthropic && to == FormatOpenAI:
		return anthropicToOpenAI(body)
	case from == FormatOpenAI && to == FormatGemini:
		return openAIToGemini(body)
	case from == FormatResponses && to == FormatOpenAI:
		return responsesToOpenAI(body)
	default:
		return nil, fmt.Errorf("%w: %s -> %s", ErrUnsupported, from, to)
	}
}

func (d *Dialect) Stream(from, to Format, upstream io.Reader, w io.Writer, flush func()) error {
	supported := false
	for _, p := range d.Supported() {
		if p.From == from && p.To == to {
			supported = true
			break
		}
	}
	if !supported {
		return fmt.Errorf("%w: %s -> %s", ErrUnsupported, from, to)
	}
	return translateStream(w, upstream, from, to, flush)
}

func (d *Dialect) Supported() []Pair {
	return []Pair{
		{From: FormatOpenAI, To: FormatAnthropic},
		{From: FormatAnthropic, To: FormatOpenAI},
		{From: FormatOpenAI, To: FormatGemini},
		{From: FormatResponses, To: FormatOpenAI},
	}
}

func ResponsesToOpenAI(body []byte) ([]byte, error) { return responsesToOpenAI(body) }
func OpenAIToResponses(body []byte, model string) ([]byte, error) {
	return openAIToResponses(body, model)
}
func GeminiToOpenAI(body []byte, model string) ([]byte, error) { return geminiToOpenAI(body, model) }
func OpenAIToGemini(body []byte) ([]byte, error)               { return openAIToGemini(body) }
