package openai

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type TTSClient struct {
	apiKey         string
	model          string
	voice          string
	responseFormat string
	hc             *http.Client
}

func NewTTSClient(apiKey, model, voice, responseFormat string) *TTSClient {
	return &TTSClient{
		apiKey:         apiKey,
		model:          model,
		voice:          voice,
		responseFormat: responseFormat,
		hc: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

func (c *TTSClient) Enabled() bool { return c.apiKey != "" }

func (c *TTSClient) Synthesize(ctx context.Context, text string) (audioID string, audioBytes []byte, contentType string, err error) {
	if c.apiKey == "" {
		return "", nil, "", fmt.Errorf("openai api key missing")
	}

	// OpenAI Audio / Speech endpoint
	// POST https://api.openai.com/v1/audio/speech
	// Models like tts-1 / tts-1-hd; voices vary by model. :contentReference[oaicite:7]{index=7}
	reqBody := map[string]any{
		"model":           c.model,
		"voice":           c.voice,
		"input":           text,
		"response_format": c.responseFormat,
	}
	b, _ := json.Marshal(reqBody)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/audio/speech", bytes.NewReader(b))
	if err != nil {
		return "", nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", nil, "", fmt.Errorf("openai tts failed: status=%d body=%s", resp.StatusCode, string(body))
	}

	audioBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, "", err
	}

	audioID = randID()
	contentType = resp.Header.Get("Content-Type")
	if contentType == "" {
		// safe default for mp3
		contentType = "audio/mpeg"
	}
	return audioID, audioBytes, contentType, nil
}

func randID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
