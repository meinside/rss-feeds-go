package rf

import (
	"log"
	"os"
	"testing"
)

// TODO: add more tests

// test summarization features
func TestSummarize(t *testing.T) {
	googleAIAPIKey := os.Getenv("API_KEY")

	if googleAIAPIKey != "" {
		client := NewClient(googleAIAPIKey, nil)
		// client.SetDesiredLanguage("en_US")
		client.SetDesiredLanguage("ko_KR")

		// summarize content and translate title
		if translatedTitle, summarizedContent, err := client.summarize(
			`meinside/rss-feeds-go: A go utility package for handling RSS feeds.`,
			`https://github.com/meinside/rss-feeds-go`,
		); err != nil {
			t.Errorf("failed to summarize: %s", err)
		} else {
			log.Printf(">>> translated title: %s", translatedTitle)
			log.Printf(">>> summarized content: %s", summarizedContent)
		}
	} else {
		log.Printf("> Provide a google ai api key: 'API_KEY' as an environment variable for testing gemini features.")
	}
}
