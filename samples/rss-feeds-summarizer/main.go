package main

import (
	"log"

	rf "github.com/meinside/rss-feeds-go"
)

const (
	googleAIAPIKey = "abcdefghijklmnopqrstuvwxyz0123456789" // NOTE: change it to yours

	googleAIModel   = "gemini-1.5-flash-latest"
	dbFilepath      = "./test.db"
	desiredLanguage = "Korean"
	//verbose         = false
	verbose = true

	noSummary = "<<< no summary >>>"
)

func main() {
	if client, err := rf.NewClientWithDB(
		googleAIAPIKey,
		[]string{
			"https://hnrss.org/newest?points=50", // hackernews' newest articles with 50+ points
			"https://lobste.rs/rss",              // lobst.rs
		},
		dbFilepath,
	); err == nil {
		client.SetGoogleAIModel(googleAIModel)
		client.SetDesiredLanguage(desiredLanguage)
		client.SetVerbose(verbose)

		if feeds, err := client.FetchFeeds(true); err == nil {
			err := client.SummarizeAndCacheFeeds(feeds)
			if err != nil {
				log.Printf("# summary failed with some errors: %s", err)
			}

			// fetch cached items,
			items := client.ListCachedItems(false)

			// print to stdout,
			for _, item := range items {
				if item.Summary == nil { // if there is no summary yet, for any reason,
					summary := noSummary
					item.Summary = &summary
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
					*item.Summary,
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
