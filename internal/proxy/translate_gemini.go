package proxy

import "github.com/mariobgsp/routre/internal/proxy/dialect"

func openAIToGemini(body []byte) ([]byte, error) { return dialect.OpenAIToGemini(body) }

func geminiToOpenAI(body []byte, model string) ([]byte, error) {
	return dialect.GeminiToOpenAI(body, model)
}

func geminiFinishToOpenAI(fr string) string { return dialect.GeminiFinishToOpenAI(fr) }
