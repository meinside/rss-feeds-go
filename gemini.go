package rf

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	// google ai
	"google.golang.org/genai"

	// my libraries
	gt "github.com/meinside/gemini-things-go"
)

const (
	defaultGoogleAIModel = "gemini-2.5-flash"

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

	summarizedContentEmpty = "Summarized content was empty."

	generationTimeoutSeconds           = 3 * 60 // timeout seconds for generation (summary + translation)
	generationTimeoutSecondsForYoutube = 5 * 60 // timeout seconds for summary of youtube video
)

// translate and summarize given things
func (c *Client) translateAndSummarize(
	ctx context.Context,
	prompt string,
	files ...[]byte,
) (translatedTitle, summarizedContent string, err error) {
	gtc, err := gt.NewClient(
		c.rotatedAPIKey(),
		gt.WithModel(c.googleAIModel),
	)
	if err != nil {
		return "", "", fmt.Errorf("error initializing gemini-things client: %w", err)
	}
	setCustomFileConverters(gtc)

	defer func() {
		if err := gtc.Close(); err != nil {
			log.Printf("failed to close gemini-things client: %s", err)
		}
	}()

	// system instruction
	gtc.SetSystemInstructionFunc(systemInstructionForTranslationAndSummary)

	// prompt & files
	promptFiles := map[string]io.Reader{}
	for i, file := range files {
		promptFiles[fmt.Sprintf("file %d", i+1)] = bytes.NewReader(file)
	}

	prompts := []gt.Prompt{gt.PromptFromText(prompt)}
	for filename, file := range promptFiles {
		prompts = append(prompts, gt.PromptFromFile(filename, file))
	}
	var contents []*genai.Content
	if contents, err = gtc.PromptsToContents(ctx, prompts, nil); err == nil {
		// set function call
		options := genOptions()

		// generate
		var result *genai.GenerateContentResponse
		if result, err = gtc.Generate(
			ctx,
			contents,
			options,
		); err == nil {
			if len(result.Candidates) > 0 {
				candidate := result.Candidates[0]

				if content := candidate.Content; content != nil {
					for _, part := range content.Parts {
						if part.FunctionCall != nil {
							fn := part.FunctionCall

							if fn.Name != fnNameTranslateTitleAndSummarizeContent {
								err = fmt.Errorf("not an expected function name: '%s'", fn.Name)
								break
							} else {
								// get trasnlated title
								if arg, e := gt.FuncArg[string](fn.Args, fnParamNameTranslatedTitle); e == nil {
									if arg != nil {
										translatedTitle = *arg
									} else {
										err = fmt.Errorf("could not find function argument '%s'", fnParamNameTranslatedTitle)
										break
									}
								} else {
									err = fmt.Errorf("could not get function argument '%s': %w", fnParamNameTranslatedTitle, e)
									break
								}

								// get summarized content
								if arg, e := gt.FuncArg[string](fn.Args, fnParamNameSummarizedContent); e == nil {
									if arg != nil {
										summarizedContent = *arg
									} else {
										err = fmt.Errorf("could not find function argument '%s'", fnParamNameSummarizedContent)
										break
									}
								} else {
									err = fmt.Errorf("could not get function argument '%s': %w", fnParamNameSummarizedContent, e)
									break
								}
							}
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
	} else {
		err = fmt.Errorf("failed to convert prompts/files to contents: %w", err)
	}

	return translatedTitle, summarizedContent, err
}

// summarize given url
func (c *Client) summarizeURL(
	ctx context.Context,
	title string,
	url string,
	desiredLanguage string,
) (untouchedTitle string, summarizedContent string, err error) {
	gtc, err := gt.NewClient(
		c.rotatedAPIKey(),
		gt.WithModel(c.googleAIModel),
	)
	if err != nil {
		return "", "", fmt.Errorf("error initializing gemini-things client: %w", err)
	}

	defer func() {
		if err := gtc.Close(); err != nil {
			log.Printf("failed to close gemini-things client: %s", err)
		}
	}()

	// system instruction
	gtc.SetSystemInstructionFunc(systemInstructionForTranslationAndSummary)

	// prompts
	prompts := []gt.Prompt{
		gt.PromptFromText(fmt.Sprintf(summarizeContentURLFormat, desiredLanguage, url)),
	}

	var contents []*genai.Content
	if contents, err = gtc.PromptsToContents(ctx, prompts, nil); err == nil {
		// use url context
		options := gt.NewGenerationOptions()
		options.Tools = []*genai.Tool{
			{
				URLContext: &genai.URLContext{},
			},
		}

		outBuffer := new(strings.Builder)

		// generate
		var result *genai.GenerateContentResponse
		if result, err = gtc.Generate(
			ctx,
			contents,
			options,
		); err == nil {
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
						err = fmt.Errorf("generation was terminated due to: %s", candidate.FinishReason)
					} else {
						err = fmt.Errorf("returned content of candidate is nil: %s", Prettify(candidate))
					}
				}
			}
		}

		summarizedContent = outBuffer.String()
	} else {
		err = fmt.Errorf("failed to convert prompts/files to contents: %w", err)
	}

	return title, summarizedContent, err
}

// translate and summarize given youtube url
func (c *Client) translateAndSummarizeYouTube(
	ctx context.Context,
	title string,
	url string,
) (translatedTitle, summarizedContent string, err error) {
	gtc, err := gt.NewClient(
		c.rotatedAPIKey(),
		gt.WithModel(c.googleAIModel),
	)
	if err != nil {
		return "", "", fmt.Errorf("error initializing gemini-things client: %w", err)
	}
	setCustomFileConverters(gtc)

	defer func() {
		if err := gtc.Close(); err != nil {
			log.Printf("failed to close gemini-things client: %s", err)
		}
	}()

	// system instruction
	gtc.SetSystemInstructionFunc(systemInstructionForTranslationAndSummary)

	// prompts
	prompts := []gt.Prompt{
		gt.PromptFromText(fmt.Sprintf(summarizeYouTubePromptFormat, c.desiredLanguage, title)),
		gt.PromptFromURI(url),
	}

	var contents []*genai.Content
	if contents, err = gtc.PromptsToContents(ctx, prompts, nil); err == nil {
		// set function call
		options := genOptions()

		// generate
		var result *genai.GenerateContentResponse
		if result, err = gtc.Generate(
			ctx,
			contents,
			options,
		); err == nil {
			if len(result.Candidates) > 0 {
				candidate := result.Candidates[0]

				if content := candidate.Content; content != nil {
					for _, part := range content.Parts {
						if part.FunctionCall != nil {
							fn := part.FunctionCall

							if fn.Name != fnNameTranslateTitleAndSummarizeContent {
								err = fmt.Errorf("not an expected function name: '%s'", fn.Name)
								break
							} else {
								// get trasnlated title
								if arg, e := gt.FuncArg[string](fn.Args, fnParamNameTranslatedTitle); e == nil {
									if arg != nil {
										translatedTitle = *arg
									} else {
										err = fmt.Errorf("could not find function argument '%s'", fnParamNameTranslatedTitle)
										break
									}
								} else {
									err = fmt.Errorf("could not get function argument '%s': %w", fnParamNameTranslatedTitle, e)
									break
								}

								// get summarized content
								if arg, e := gt.FuncArg[string](fn.Args, fnParamNameSummarizedContent); e == nil {
									if arg != nil {
										summarizedContent = *arg
									} else {
										err = fmt.Errorf("could not find function argument '%s'", fnParamNameSummarizedContent)
										break
									}
								} else {
									err = fmt.Errorf("could not get function argument '%s': %w", fnParamNameSummarizedContent, e)
									break
								}
							}
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
	} else {
		err = fmt.Errorf("failed to convert prompts/files to contents: %w", err)
	}

	return translatedTitle, summarizedContent, err
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
func genOptions() *gt.GenerationOptions {
	options := gt.NewGenerationOptions()
	options.Tools = []*genai.Tool{
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
								Nullable:    genai.Ptr(false),
							},
							fnParamNameSummarizedContent: {
								Description: fnParamDescSummarizedContent,
								Type:        genai.TypeString,
								Nullable:    genai.Ptr(false),
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
	}
	options.ToolConfig = &genai.ToolConfig{
		FunctionCallingConfig: &genai.FunctionCallingConfig{
			Mode: genai.FunctionCallingConfigModeAny,
			AllowedFunctionNames: []string{
				fnNameTranslateTitleAndSummarizeContent,
			},
		},
	}

	return options
}

// generate a system instruction with given configuration
func systemInstructionForTranslationAndSummary() string {
	return fmt.Sprintf(systemInstructionFormatForSummary,
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
