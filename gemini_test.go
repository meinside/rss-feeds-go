package rf

import (
	"context"
	"log"
	"os"
	"testing"
)

// test gemini features
func TestGemini(t *testing.T) {
	googleAIAPIKey := os.Getenv("API_KEY")

	if googleAIAPIKey != "" {
		client := NewClient(googleAIAPIKey, nil)

		// generate
		if generated, err := client.generate(context.TODO(), 0, "Summarize the content of the following url: https://github.com/meinside/rss-feeds-go"); err != nil {
			t.Errorf("failed to generate with gemini: %s", err)
		} else {
			log.Printf(">>> generated: %s", generated)
		}
	} else {
		log.Printf("> Provide a google ai api key: 'API_KEY' as an environment variable for testing gemini features.")
	}
}
