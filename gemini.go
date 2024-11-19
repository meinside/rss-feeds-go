package rf

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	// google ai
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/googleapi"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
)

const (
	defaultGoogleAIModel    = "gemini-1.5-flash-latest"
	systemInstructionFormat = `You are a chat bot for summarizing contents retrieved from web sites or RSS feeds.

Current datetime is %[1]s.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
`
	summarizeURLPromptFormat = `Summarize the content of following <link></link> tag in %[1]s language:

%[2]s`
	summarizeFilePromptFormat = `Summarize the content of attached file(s) in %[1]s language.`

	generationTimeoutSeconds = 60 // 1 minute

	sleepSecondsBeforeRetry = 5
)

// generate with given things
func (c *Client) generate(ctx context.Context, remainingRetryCount int, prompt string, files ...[]byte) (generated string, err error) {
	ctx, cancel := context.WithTimeout(ctx, generationTimeoutSeconds*time.Second)
	defer cancel()

	gtc, err := gt.NewClient(c.googleAIModel, c.googleAIAPIKey)
	if err != nil {
		return "", fmt.Errorf("error initializing gemini-things client: %s", err)
	}
	defer gtc.Close()
	gtc.SetTimeout(generationTimeoutSeconds)

	// system instruction
	gtc.SetSystemInstructionFunc(defaultSystemInstruction)

	// prompt & files
	promptFiles := map[string]io.Reader{}
	for i, file := range files {
		promptFiles[fmt.Sprintf("file %d", i+1)] = bytes.NewReader(file)
	}

	// generate
	var result *genai.GenerateContentResponse
	if result, err = gtc.Generate(ctx, prompt, promptFiles); err == nil {
		if len(result.Candidates) > 0 {
			candidate := result.Candidates[0]

			if content := candidate.Content; content != nil {
				for _, part := range content.Parts {
					if text, ok := part.(genai.Text); ok { // (text)
						generated += string(text)
					} else {
						err = fmt.Errorf("unsupported type of part from generation: %s", Prettify(part))
					}
				}
			} else {
				if candidate.FinishReason != genai.FinishReasonUnspecified {
					err = fmt.Errorf("generation was terminated due to: %s", candidate.FinishReason.String())
				} else {
					err = fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
				}
			}
		}
	} else {
		// retry on google server errors
		var gerr *googleapi.Error
		if errors.As(err, &gerr) && gerr.Code >= 500 && remainingRetryCount > 0 {
			time.Sleep(time.Duration(sleepSecondsBeforeRetry) * time.Second) // NOTE: sleep before retrying

			return c.generate(ctx, remainingRetryCount-1, prompt, files...)
		}
	}

	return generated, err
}

// generate a default system instruction with given configuration
func defaultSystemInstruction() string {
	return fmt.Sprintf(systemInstructionFormat,
		time.Now().Format("2006-01-02 15:04:05 (Mon) MST"),
	)
}
