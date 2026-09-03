package model_test

import (
	"time"

	"github.com/example/agent-sdk/model"
	"github.com/example/agent-sdk/model/openai"
)

// This compile-only example shows the recommended production composition:
// timeout and retry on every candidate, rate limiting on the primary, and a
// fallback chain for provider outages.
func Example_productionModel() {
	primary := model.WithRateLimit(
		model.WithRetry(
			model.WithTimeout(openai.New(openai.Config{
				APIKey:       "primary-key",
				DefaultModel: "gpt-4o-mini",
			}), 60*time.Second),
			model.DefaultRetryPolicy(),
		),
		5, 5,
	)
	backup := model.WithRetry(
		model.WithTimeout(openai.New(openai.Config{
			APIKey:       "backup-key",
			BaseURL:      "https://backup.example.com/v1",
			DefaultModel: "gpt-4o-mini",
		}), 60*time.Second),
		model.DefaultRetryPolicy(),
	)

	var m model.Model = model.Fallback(primary, backup)
	_ = m
}
