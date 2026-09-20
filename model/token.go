package model

import (
	"errors"
	"fmt"
)

// ErrUnsupportedTokenModel indicates that a local token counter has no exact
// mapping for a model. Callers should inject a provider-specific TokenCounter
// rather than treating this as a token count of zero.
var ErrUnsupportedTokenModel = errors.New("model: unsupported local token-counting model")

// UnsupportedTokenModelError reports the model for which local token counting
// is unavailable.
type UnsupportedTokenModelError struct {
	ModelName string
}

func (e *UnsupportedTokenModelError) Error() string {
	if e.ModelName == "" {
		return "model: local token counting is unsupported; inject a provider-specific TokenCounter"
	}
	return fmt.Sprintf("model: local token counting is unsupported for model %q; inject a provider-specific TokenCounter", e.ModelName)
}

// Is lets callers test errors with errors.Is(err, ErrUnsupportedTokenModel).
func (e *UnsupportedTokenModelError) Is(target error) bool {
	return target == ErrUnsupportedTokenModel
}

// TokenCounter produces local token estimates for a named model.
//
// Provider adapters or applications inject an implementation with the exact
// tokenizer and message framing rules for the target model. Its results are
// local estimates, not provider usage: do not copy them into Usage, which only
// records token counts reported by a provider response.
type TokenCounter interface {
	// CountText returns the local token estimate for text under modelName.
	CountText(modelName, text string) (int, error)
	// CountMessages returns the local token estimate for messages, including any
	// provider-specific framing, tool-call, and multimodal accounting.
	CountMessages(modelName string, messages []Message) (int, error)
}
