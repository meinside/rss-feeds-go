package rf

import (
	"context"
	"log"
	"os"
	"testing"
	"time"
)

// TODO: add more tests

// test summarization features
func TestSummarize(t *testing.T) {
	googleAIAPIKey := os.Getenv("GEMINI_API_KEY")

	if googleAIAPIKey != "" {
		client := NewClient([]string{googleAIAPIKey}, nil)
		// client.SetDesiredLanguage("en_US")
		client.SetDesiredLanguage("ko_KR")

		// summarize content and translate title
		ctx, cancel := context.WithTimeout(context.TODO(), 60*time.Second)
		defer cancel()
		if _, translatedTitle, summarizedContent, err := client.summarize(
			ctx,
			`meinside/rss-feeds-go: A go utility package for handling RSS feeds.`,
			`https://github.com/meinside/rss-feeds-go`,
		); err != nil {
			t.Errorf("failed to summarize url content: %s", err)
		} else {
			log.Printf(">>> translated title: %s", translatedTitle)
			log.Printf(">>> summarized content: %s", summarizedContent)
		}

		// summarize url
		ctx, cancel = context.WithTimeout(context.TODO(), 60*time.Second)
		defer cancel()
		if _, translatedTitle, summarizedContent, err := client.summarizeURL(
			ctx,
			`meinside/gemini-things-go: A Golang library for generating things with Gemini APIs `,
			`https://github.com/meinside/gemini-things-go`,
			`ko_KR`,
		); err != nil {
			t.Errorf("failed to summarize url: %s", err)
		} else {
			log.Printf(">>> translated title (untouched): %s", translatedTitle)
			log.Printf(">>> summarized content (with gemini url context): %s", summarizedContent)
		}

		// summarize youtube url and translate title
		ctx, cancel = context.WithTimeout(context.TODO(), 60*time.Second)
		defer cancel()
		if _, translatedTitle, summarizedContent, err := client.summarize(
			ctx,
			`I2C test on Raspberry Pi with Adafruit 8x8 LED Matrix and Ruby`,
			`https://www.youtube.com/watch?v=fV5rI_5fDI8`,
		); err != nil {
			t.Errorf("failed to summarize youtube url: %s", err)
		} else {
			log.Printf(">>> translated title: %s", translatedTitle)
			log.Printf(">>> summarized content: %s", summarizedContent)
		}

		// wrong urls can be successfully summarized with gemini url context
		// (but there will be error messages from gemini)
		ctx, cancel = context.WithTimeout(context.TODO(), 60*time.Second)
		defer cancel()
		if _, translatedTitle, summarizedContent, err := client.summarize(
			ctx,
			`What is the answer to life, the universe, and everything?`,
			`https://no-sucn-domain/that-will-lead/to/fetch-error`,
		); err != nil {
			t.Errorf("should have failed with the wrong url")
		} else {
			if translatedTitle != `What is the answer to life, the universe, and everything?` {
				t.Errorf("should have kept the title, but got '%s'", translatedTitle)
			}
			log.Printf(">>> translated title (untouched): %s", translatedTitle)
			log.Printf(">>> summarized content (error message): %s", summarizedContent)
		}

		// when gemini fails,
		// (will keep the original title)
		ctx, cancel = context.WithTimeout(context.TODO(), 60*time.Second)
		defer cancel()
		client.googleAIAPIKeys = []string{"intentionally-wrong-api-key"}
		if _, translatedTitle, summarizedContent, err := client.summarize(
			ctx,
			`What is the answer to life, the universe, and everything?`,
			`https://no-sucn-domain/that-will-lead/to/fetch-error`,
		); err != nil {
			if translatedTitle != `What is the answer to life, the universe, and everything?` {
				t.Errorf("should have kept the title, but got '%s'", translatedTitle)
			}
			log.Printf(">>> translated title (untouched): %s", translatedTitle)
			log.Printf(">>> summarized content (api error): %s", summarizedContent)
		} else {
			t.Errorf("should have failed with the wrong url")
		}

	} else {
		log.Printf("> Provide a google ai api key: 'GEMINI_API_KEY' as an environment variable for testing gemini features.")
	}
}
