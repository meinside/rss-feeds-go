package rf

import (
	"context"
	"fmt"
	"time"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const (
	defaultGoogleAIModel    = "gemini-1.5-flash-latest"
	systemInstructionFormat = `You are a chat bot for summarizing web contents, which is built with Golang and Google Gemini API(model: %[1]s).

Current datetime is %[2]s.

Respond to user messages according to the following principles:
- Do not repeat the user's request.
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
- Be as concise and meaningful as possible.
- Your response must be in plain text, so do not try to emphasize words with markdown characters.
`
	summaryPromptFormat = `Summarize the content of following <link></link> tag in %[1]s language:

%[2]s`

	timeoutSeconds = 60 // 1 minute
)

type role string

const (
	roleModel role = "model"
	roleUser  role = "user"
)

// generate with given things
func (c *Client) generate(ctx context.Context, prompt string) (generated string, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeoutSeconds*time.Second)
	defer cancel()

	var client *genai.Client
	client, err = genai.NewClient(ctx, option.WithAPIKey(c.googleAIAPIKey))
	if err != nil {
		return generated, fmt.Errorf("failed to create Google AI API client: %s", err)
	}
	defer client.Close()

	model := client.GenerativeModel(defaultGoogleAIModel)

	// system instruction
	model.SystemInstruction = &genai.Content{
		Role: string(roleModel),
		Parts: []genai.Part{
			genai.Text(defaultSystemInstruction()),
		},
	}

	// safety filters (block only high)
	model.SafetySettings = safetySettings(genai.HarmBlockThreshold(genai.HarmBlockOnlyHigh))

	// generate
	var result *genai.GenerateContentResponse
	if result, err = model.GenerateContent(ctx, genai.Text(prompt)); err == nil {
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
				err = fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
			}
		}
	}

	return generated, err
}

// generate safety settings for all supported harm categories
func safetySettings(threshold genai.HarmBlockThreshold) (settings []*genai.SafetySetting) {
	for _, category := range []genai.HarmCategory{
		/*
			// categories for PaLM 2 (Legacy) models
			genai.HarmCategoryUnspecified,
			genai.HarmCategoryDerogatory,
			genai.HarmCategoryToxicity,
			genai.HarmCategoryViolence,
			genai.HarmCategorySexual,
			genai.HarmCategoryMedical,
			genai.HarmCategoryDangerous,
		*/

		// all categories supported by Gemini models
		genai.HarmCategoryHarassment,
		genai.HarmCategoryHateSpeech,
		genai.HarmCategorySexuallyExplicit,
		genai.HarmCategoryDangerousContent,
	} {
		settings = append(settings, &genai.SafetySetting{
			Category:  category,
			Threshold: threshold,
		})
	}

	return settings
}

// generate a default system instruction with given configuration
func defaultSystemInstruction() string {
	datetime := time.Now().Format("2006-01-02 15:04:05 (Mon)")

	return fmt.Sprintf(systemInstructionFormat,
		defaultGoogleAIModel,
		datetime,
	)
}
