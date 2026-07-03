package rf

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
)

const (
	defaultGoogleAIModel = "gemini-3-flash-preview"

	systemInstructionFormatForSummary = `You are a precise and useful agent for summarizing and translating contents retrieved from web sites or RSS/Atom feeds.

Current datetime is %[1]s.

Respond to user messages according to the following principles:
- Be as accurate as possible.
- Be as truthful as possible.
- Be as comprehensive and informative as possible.
- Try to keep the nuances of the original title and/or content as much as possible.
- If the title is already in the same language, or too vague to be translated, just keep it as it is.
`
	summarizeContentPromptFormat = `Summarize the content of following <content:link></content:link> tag in %[1]s language,
and translate the title of the content in <content:title></content:title> tag into the same language referring to the summarized content.
If the content implies an error such as network or permission issues, do not translate the title and keep it as is.

<content:title>%[2]s</content:title>
<content:link>%[3]s</content:link>`
	summarizeContentFilePromptFormat = `Summarize the content of attached file(s) in %[1]s language,
and translate the title of the content in <content:title></content:title> tag into the same language
referring to the summarized content:

<content:title>%[2]s</content:title>`
	summarizeContentURLFormat = `Summarize the url of following <content:link></content:link> tag in %[1]s language.

<content:link>%[2]s</content:link>`

	summarizeYouTubePromptFormat = `Summarize the content of given YouTube video in %[1]s language,
and translate the title of the content in <content:title></content:title> tag into the same language
referring to the summarized content:

<content:title>%[2]s</content:title>`

	summarizedContentEmpty = `Summarized content was empty.`

	requestTimeoutSeconds              = 30
	generationTimeoutSeconds           = 3 * 60 // timeout seconds for generation (summary + translation)
	generationTimeoutSecondsForYoutube = 5 * 60 // timeout seconds for summary of youtube video
)

// markCooldown records a cooldown expiry for the given combo index based on
// the quota error's RetryInfo (falling back to the default).
func (c *Client) markCooldown(idx int, err error, now time.Time) {
	c.cooldownMu.Lock()
	defer c.cooldownMu.Unlock()
	c.cooldownUntil[idx] = now.Add(cooldownDuration(err))
}

// newGeminiClientForCombo builds a gemini-things client for a specific combo
// and installs the system instruction. Caller must close the returned client.
func (c *Client) newGeminiClientForCombo(combo keyModelCombo) (gtc *gt.Client, err error) {
	gtc, err = gt.NewClient(
		combo.apiKey,
		gt.WithModel(combo.model),
	)
	if err != nil {
		return nil, fmt.Errorf("error initializing gemini-things client: %w", err)
	}
	gtc.SetSystemInstructionFunc(systemInstructionForTranslationAndSummary)
	return gtc, nil
}

// withFailover picks an available combo, runs `run`, and on a quota (429)
// error marks that combo's cooldown and retries with the next available
// combo. Non-429 errors are returned immediately. Returns ErrNoAvailableAPIKey
// if every combo is exhausted or in cooldown.
func (c *Client) withFailover(
	now func() time.Time,
	run func(gtc *gt.Client, model string) error,
) (usedModel string, err error) {
	attempts := len(c.combos)
	for i := 0; i < attempts; i++ {
		combo, idx, ok := c.pickAvailableCombo(now())
		if !ok {
			return usedModel, ErrNoAvailableAPIKey
		}
		usedModel = combo.model

		gtc, cerr := c.newGeminiClientForCombo(combo)
		if cerr != nil {
			return usedModel, cerr
		}

		runErr := run(gtc, combo.model)
		closeGeminiClient(gtc)

		if runErr == nil {
			return usedModel, nil
		}
		if gt.IsQuotaExceeded(runErr) {
			c.markCooldown(idx, runErr, now())
			continue
		}
		return usedModel, runErr
	}
	return usedModel, ErrNoAvailableAPIKey
}

// closeGeminiClient closes the gemini-things client, logging any errors.
func closeGeminiClient(gtc *gt.Client) {
	if err := gtc.Close(); err != nil {
		log.Printf("failed to close gemini-things client: %s", err)
	}
}

// translate and summarize given things
func (c *Client) translateAndSummarize(
	ctx context.Context,
	prompt string,
	files ...[]byte,
) (usedModel, translatedTitle, summarizedContent string, err error) {
	buffer := strings.Builder{}

	usedModel, err = c.withFailover(time.Now, func(gtc *gt.Client, model string) error {
		buffer.Reset()
		translatedTitle = ""
		setCustomFileConverters(gtc)

		// prompt & files
		prompts := []gt.Prompt{gt.PromptFromText(prompt)}
		for i, file := range files {
			prompts = append(prompts, gt.PromptFromFile(fmt.Sprintf("file %d", i+1), bytes.NewReader(file)))
		}

		// context with timeout (prompts => contents)
		ctxContents, cancelContents := context.WithTimeout(ctx, time.Duration(requestTimeoutSeconds)*time.Second)
		defer cancelContents()

		contents, cerr := gtc.PromptsToContents(ctxContents, prompts, nil)
		if cerr != nil {
			return fmt.Errorf("failed to convert prompts/files to contents: %w", cerr)
		}

		// set function call
		options := genOptions()

		// context with timeout (generation)
		ctxGenerate, cancelGenerate := context.WithTimeout(ctx, time.Duration(generationTimeoutSeconds)*time.Second)
		defer cancelGenerate()

		result, gerr := gtc.Generate(ctxGenerate, contents, options)
		if gerr != nil {
			return gerr
		}

		for _, cand := range result.Candidates {
			if cand.Content != nil {
				for _, part := range cand.Content.Parts {
					if part.FunctionCall != nil {
						title, content, xerr := extractTranslatedTitleAndSummarizedContent(part.FunctionCall)
						if xerr != nil {
							return xerr
						}
						translatedTitle = title
						buffer.WriteString(content)
					} else {
						// FIXME: sometimes there is no function call but text in returned part
						if len(part.Text) > 0 {
							buffer.WriteString(part.Text) // append text
						} else {
							return fmt.Errorf("no function call nor text in part: %s", Prettify(part))
						}
					}
				}
			} else {
				if cand.FinishReason != genai.FinishReasonUnspecified {
					return fmt.Errorf("generation was terminated due to: %s", cand.FinishReason)
				}
				return fmt.Errorf("returned content of candidate is nil: %s", Prettify(cand))
			}
		}

		if buffer.Len() <= 0 {
			return fmt.Errorf("summarized content was empty [%s]", model)
		}
		return nil
	})

	return usedModel, translatedTitle, buffer.String(), err
}

// summarize given url
func (c *Client) summarizeURL(
	ctx context.Context,
	title string,
	url string,
	desiredLanguage string,
) (usedModel, untouchedTitle string, summarizedContent string, err error) {
	outBuffer := new(strings.Builder)

	usedModel, err = c.withFailover(time.Now, func(gtc *gt.Client, model string) error {
		outBuffer.Reset()

		// prompts
		prompts := []gt.Prompt{
			gt.PromptFromText(fmt.Sprintf(summarizeContentURLFormat, desiredLanguage, url)),
		}

		// context with timeout (prompts => contents)
		ctxContents, cancelContents := context.WithTimeout(ctx, requestTimeoutSeconds*time.Second)
		defer cancelContents()

		contents, cerr := gtc.PromptsToContents(ctxContents, prompts, nil)
		if cerr != nil {
			return fmt.Errorf("failed to convert prompts/files to contents: %w", cerr)
		}

		// use url context
		options := &genai.GenerateContentConfig{
			Tools: []*genai.Tool{
				{
					URLContext: &genai.URLContext{},
				},
			},
		}

		// context with timeout (generation)
		ctxGenerate, cancelGenerate := context.WithTimeout(ctx, generationTimeoutSeconds*time.Second)
		defer cancelGenerate()

		result, gerr := gtc.Generate(ctxGenerate, contents, options)
		if gerr != nil {
			return gerr
		}

		if len(result.Candidates) > 0 {
			candidate := result.Candidates[0]

			if content := candidate.Content; content != nil {
				for _, part := range content.Parts {
					if len(part.Text) > 0 {
						outBuffer.WriteString(part.Text)
					}
				}
			} else {
				if candidate.FinishReason != genai.FinishReasonUnspecified {
					return fmt.Errorf("generation was terminated due to: %s", candidate.FinishReason)
				}
				return fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
			}
		}

		if outBuffer.Len() <= 0 {
			return fmt.Errorf("summarized content was empty")
		}
		return nil
	})

	return usedModel, title, outBuffer.String(), err
}

// translate and summarize given youtube url
func (c *Client) translateAndSummarizeYouTube(
	ctx context.Context,
	title string,
	url string,
) (usedModel, translatedTitle, summarizedContent string, err error) {
	usedModel, err = c.withFailover(time.Now, func(gtc *gt.Client, model string) error {
		translatedTitle, summarizedContent = "", ""
		setCustomFileConverters(gtc)

		// prompts
		prompts := []gt.Prompt{
			gt.PromptFromText(fmt.Sprintf(summarizeYouTubePromptFormat, c.desiredLanguage, title)),
			gt.PromptFromURI(url, `video/mp4`),
		}

		contents, cerr := gtc.PromptsToContents(ctx, prompts, nil)
		if cerr != nil {
			return fmt.Errorf("failed to convert prompts/files to contents: %w", cerr)
		}

		// set function call
		options := genOptions()

		result, gerr := gtc.Generate(ctx, contents, options)
		if gerr != nil {
			return gerr
		}

		if len(result.Candidates) > 0 {
			candidate := result.Candidates[0]

			if content := candidate.Content; content != nil {
				for _, part := range content.Parts {
					if part.FunctionCall != nil {
						if translatedTitle, summarizedContent, cerr = extractTranslatedTitleAndSummarizedContent(part.FunctionCall); cerr != nil {
							return cerr
						}
					}
				}
			} else {
				if candidate.FinishReason != genai.FinishReasonUnspecified {
					return fmt.Errorf("generation was terminated due to: %s", candidate.FinishReason)
				}
				return fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
			}
		}
		return nil
	})

	return usedModel, translatedTitle, summarizedContent, err
}

// extractTranslatedTitleAndSummarizedContent extracts translated title and summarized content from a function call
func extractTranslatedTitleAndSummarizedContent(fn *genai.FunctionCall) (translatedTitle, summarizedContent string, err error) {
	if fn.Name != fnNameTranslateTitleAndSummarizeContent {
		return "", "", fmt.Errorf("not an expected function name: '%s'", fn.Name)
	}

	// get translated title
	if arg, e := gt.FuncArg[string](fn.Args, fnParamNameTranslatedTitle); e == nil {
		if arg != nil {
			translatedTitle = *arg
		} else {
			return "", "", fmt.Errorf("could not find function argument '%s'", fnParamNameTranslatedTitle)
		}
	} else {
		return "", "", fmt.Errorf("could not get function argument '%s': %w", fnParamNameTranslatedTitle, e)
	}

	// get summarized content
	if arg, e := gt.FuncArg[string](fn.Args, fnParamNameSummarizedContent); e == nil {
		if arg != nil {
			summarizedContent = *arg
		} else {
			return "", "", fmt.Errorf("could not find function argument '%s'", fnParamNameSummarizedContent)
		}
	} else {
		return "", "", fmt.Errorf("could not get function argument '%s': %w", fnParamNameSummarizedContent, e)
	}

	return translatedTitle, summarizedContent, nil
}

const (
	fnNameTranslateTitleAndSummarizeContent = "translateTitleAndSummarizeContent"
	fnDescTranslateTitleAndSummarizeContent = `Summarize the given content and translate the title referring to the summarized content.`
	fnParamNameTranslatedTitle              = "translatedTitle"
	fnParamDescTranslatedTitle              = `Translated title of the content.`
	fnParamNameSummarizedContent            = "summarizedContent"
	fnParamDescSummarizedContent            = `Summarized content.`
)

// options for generation
func genOptions() *genai.GenerateContentConfig {
	return &genai.GenerateContentConfig{
		Tools: []*genai.Tool{
			{
				FunctionDeclarations: []*genai.FunctionDeclaration{
					{
						Name:        fnNameTranslateTitleAndSummarizeContent,
						Description: fnDescTranslateTitleAndSummarizeContent,
						Parameters: &genai.Schema{
							Type: genai.TypeObject,
							Properties: map[string]*genai.Schema{
								fnParamNameTranslatedTitle: {
									Description: fnParamDescTranslatedTitle,
									Type:        genai.TypeString,
									Nullable:    new(false),
								},
								fnParamNameSummarizedContent: {
									Description: fnParamDescSummarizedContent,
									Type:        genai.TypeString,
									Nullable:    new(false),
								},
							},
							Required: []string{
								fnParamNameTranslatedTitle,
								fnParamNameSummarizedContent,
							},
						},
					},
				},
			},
		},
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAny,
				AllowedFunctionNames: []string{
					fnNameTranslateTitleAndSummarizeContent,
				},
			},
		},
	}
}

// generate a system instruction with given configuration
func systemInstructionForTranslationAndSummary() string {
	return fmt.Sprintf(systemInstructionFormatForSummary,
		time.Now().Format("2006-01-02 15:04:05 (Mon) MST"),
	)
}

// set custom file converters
func setCustomFileConverters(gtc *gt.Client) {
	gtc.SetFileConverter(`application/xhtml+xml`, func(filename string, bytes []byte) ([]gt.ConvertedFile, error) {
		// do nothing but override the content type ("application/xhtml+xml" => "text/html")
		return []gt.ConvertedFile{
			{
				Filename: filename,
				Bytes:    bytes,
				MimeType: `text/html`,
			},
		}, nil
	})

	// TODO: add more converters here
}
