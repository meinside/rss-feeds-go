package rf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
)

const (
	defaultGoogleAIModel    = "gemini-2.0-flash"
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
)

// generate with given things
func (c *Client) generate(ctx context.Context, prompt string, files ...[]byte) (generated string, err error) {
	ctx, cancel := context.WithTimeout(ctx, generationTimeoutSeconds*time.Second)
	defer cancel()

	gtc, err := gt.NewClient(c.googleAIAPIKey, c.googleAIModel)
	if err != nil {
		return "", fmt.Errorf("error initializing gemini-things client: %w", err)
	}
	gtc.SetTimeout(generationTimeoutSeconds)
	setCustomFileConverters(gtc)
	defer gtc.Close()

	// system instruction
	gtc.SetSystemInstructionFunc(defaultSystemInstruction)

	// prompt & files
	promptFiles := map[string]io.Reader{}
	for i, file := range files {
		promptFiles[fmt.Sprintf("file %d", i+1)] = bytes.NewReader(file)
	}

	prompts := []gt.Prompt{gt.PromptFromText(prompt)}
	for filename, file := range promptFiles {
		prompts = append(prompts, gt.PromptFromFile(filename, file))
	}

	// generate
	var result *genai.GenerateContentResponse
	if result, err = gtc.Generate(
		ctx,
		prompts,
		gt.NewGenerationOptions(),
	); err == nil {
		if len(result.Candidates) > 0 {
			candidate := result.Candidates[0]

			if content := candidate.Content; content != nil {
				for _, part := range content.Parts {
					if part.Text != "" {
						generated += part.Text
					} else {
						err = fmt.Errorf("unsupported type of part from generation: %s", Prettify(part))
					}
				}
			} else {
				if candidate.FinishReason != genai.FinishReasonUnspecified {
					err = fmt.Errorf("generation was terminated due to: %s", candidate.FinishReason)
				} else {
					err = fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
				}
			}
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

// set custom file converters
func setCustomFileConverters(gtc *gt.Client) {
	gtc.SetFileConverter("application/xhtml+xml", func(bytes []byte) ([]byte, string, error) {
		// do nothing but override the content type ("application/xhtml+xml" => "text/html")
		return bytes, "text/html", nil
	})

	// TODO: add more converters here
}
