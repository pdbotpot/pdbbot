// Package llm provides an OpenAI-compatible chat completion client.
// Works with local llama-server and Groq (both expose POST /v1/chat/completions).
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
)

const Silence = "[silent]"

type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type Client struct {
	baseURL string
	model   string
	apiKey  string
	hc      *http.Client
}

// New creates a client. apiKey is used directly; if empty, apiKeyEnv is read
// from the environment. If both are empty, no auth header is sent (local llama-server).
func New(baseURL, model, apiKey, apiKeyEnv string) *Client {
	key := apiKey
	if key == "" && apiKeyEnv != "" {
		key = os.Getenv(apiKeyEnv)
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		apiKey:  key,
		hc:      &http.Client{},
	}
}

// Reply sends systemPrompt + history to the model and returns the response text.
// Returns Silence if the model opts out or the response is empty.
func (c *Client) Reply(ctx context.Context, systemPrompt string, history []Msg) (string, error) {
	msgs := make([]Msg, 0, 1+len(history))
	msgs = append(msgs, Msg{Role: "system", Content: systemPrompt})
	msgs = append(msgs, history...)

	reqBody, _ := json.Marshal(map[string]any{
		"model":       c.model,
		"messages":    msgs,
		"temperature": 0.8,
		"max_tokens":  500,
		"stop":        []string{"\n\n"},
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/chat/completions", bytes.NewReader(reqBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llm status %d: %s", resp.StatusCode, raw)
	}

	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			CompletionTokensDetails struct {
				ReasoningTokens int `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("llm decode: %w", err)
	}
	if out.Error != nil {
		return "", errors.New("llm error: " + out.Error.Message)
	}
	if out.Usage.TotalTokens > 0 {
		if out.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
			slog.Info("llm tokens",
				"prompt", out.Usage.PromptTokens,
				"reasoning", out.Usage.CompletionTokensDetails.ReasoningTokens,
				"completion", out.Usage.CompletionTokens-out.Usage.CompletionTokensDetails.ReasoningTokens,
				"total", out.Usage.TotalTokens)
		} else {
			slog.Info("llm tokens",
				"prompt", out.Usage.PromptTokens,
				"completion", out.Usage.CompletionTokens,
				"total", out.Usage.TotalTokens)
		}
	}
	if len(out.Choices) == 0 || out.Choices[0].Message.Content == "" {
		return Silence, nil
	}
	return strings.TrimSpace(out.Choices[0].Message.Content), nil
}
