package rf

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gabriel-vasile/mimetype"
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
	summarizeURLPromptFormat = `Summarize the content of following <link></link> tag in %[1]s language:

%[2]s`
	summarizeFilePromptFormat = `Summarize the content of attached file(s) in %[1]s language.`

	timeoutSeconds = 60 // 1 minute
)

type role string

const (
	roleModel role = "model"
	roleUser  role = "user"
)

// generate with given things
func (c *Client) generate(ctx context.Context, prompt string, files ...[]byte) (generated string, err error) {
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

	// prompt
	prompts := []genai.Part{
		genai.Text(prompt),
	}

	// files
	fileNames := []string{}
	var mimeType string
	for _, file := range files {
		mimeType = stripCharsetFromMimeType(mimetype.Detect(file).String())

		if file, err := client.UploadFile(ctx, "", bytes.NewReader(file), &genai.UploadFileOptions{
			MIMEType: mimeType,
		}); err == nil {
			prompts = append(prompts, genai.FileData{
				MIMEType: file.MIMEType,
				URI:      file.URI,
			})

			fileNames = append(fileNames, file.Name) // FIXME: will wait synchronously for it to become active
		} else {
			log.Printf("failed to upload file(%s) for prompt: %s", mimeType, err)
		}
	}

	// FIXME: wait for all files to become active
	waitForFiles(ctx, client, fileNames)

	// generate
	var result *genai.GenerateContentResponse
	if result, err = model.GenerateContent(ctx, prompts...); err == nil {
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

// wait for all files to be active
func waitForFiles(ctx context.Context, client *genai.Client, fileNames []string) {
	var wg sync.WaitGroup
	for _, fileName := range fileNames {
		wg.Add(1)

		go func(name string) {
			for {
				if file, err := client.GetFile(ctx, name); err == nil {
					if file.State == genai.FileStateActive {
						wg.Done()
						break
					} else {
						time.Sleep(300 * time.Millisecond)
					}
				} else {
					log.Printf("failed to get file: %s", err)

					time.Sleep(300 * time.Millisecond)
				}
			}
		}(fileName)
	}
	wg.Wait()
}

// remove trailing charset or etc. from given mime type string
func stripCharsetFromMimeType(mimeType string) string {
	splitted := strings.Split(mimeType, ";")
	return strings.TrimSpace(splitted[0])
}
