package main

import (
	"context"
	"log"
	"time"

	rf "github.com/meinside/rss-feeds-go"
)

const (
	googleAIAPIKey = "abcdefghijklmnopqrstuvwxyz0123456789" // NOTE: change it to yours

	googleAIModel   = "gemini-2.5-flash"
	dbFilepath      = "./test.db"
	desiredLanguage = "Korean"
	// verbose         = false
	verbose = true

	noSummary = "<<< no summary >>>"

	fetchTimeoutSeconds = 30

	ignoreItemsOlderThanDays = 7
)

func main() {
	if client, err := rf.NewClientWithDB(
		[]string{googleAIAPIKey},
		[]string{
			"https://hnrss.org/newest?points=50", // hackernews' newest articles with 50+ points
			"https://lobste.rs/rss",              // lobst.rs
			"https://www.hackster.io/news.atom",  // hackster.io
		},
		dbFilepath,
	); err == nil {
		client.SetGoogleAIModel(googleAIModel)
		client.SetDesiredLanguage(desiredLanguage)
		client.SetVerbose(verbose)

		// context with timeout (fetch)
		ctxFetch, cancelFetch := context.WithTimeout(context.Background(), fetchTimeoutSeconds*time.Second)
		defer cancelFetch()

		if feeds, err := client.FetchFeeds(ctxFetch, true, ignoreItemsOlderThanDays); err == nil {
			err := client.SummarizeAndCacheFeeds(feeds)
			if err != nil {
				log.Printf("# summary failed with some errors: %s", err)
			}

			// fetch cached items,
			items := client.ListCachedItems(false)

			// print to stdout,
			for _, item := range items {
				if len(item.Summary) <= 0 { // if there is no summary yet, for any reason,
					item.Summary = noSummary
				}

				log.Printf(`
>>> Title: %[1]s
>>> Date: %[2]s

>>> Summary:

%[3]s

----
`,
					item.Title,
					item.PublishDate,
					item.Summary,
				)
			}

			log.Printf(">>> fetched %d new item(s).", len(items))

			// and mark as read
			client.MarkCachedItemsAsRead(items)

			log.Printf(">>> marked %d item(s) as read.", len(items))
		} else {
			log.Printf("# failed to fetch feeds: %s", err)
		}

		// delete old caches
		client.DeleteOldCachedItems()
	} else {
		log.Printf("# failed to create a client: %s", err)
	}
}
