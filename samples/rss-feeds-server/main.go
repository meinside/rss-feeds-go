package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	rf "github.com/meinside/rss-feeds-go"
)

const (
	googleAIAPIKey = "---not-needed---"
	dbFilepath     = "./test.db"

	httpPort       = 10101
	rssTitle       = "Testing RSS server"
	rssLink        = "https://github.com/meinside/rss-feeds-go"
	rssDescription = "Testing my RSS server..."
	rssAuthor      = "meinside"
	rssEmail       = "email@domain.com"

	rssPollerAgent = "Feedly/1.0"

	// verbose         = false
	verbose = true
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
		client.SetVerbose(verbose)

		// delete old caches
		client.DeleteOldCachedItems()

		// http handler
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if agent := r.Header.Get("User-Agent"); !strings.Contains(agent, rssPollerAgent) {
				log.Printf("# dropping access from unwanted agent: %s", agent)
				return
			}

			// fetch cached items,
			items := client.ListCachedItems(true)

			w.Header().Set("Content-Type", "application/rss+xml")
			w.Header().Set("Cache-Control", "max-age=60")

			// generate xml and serve it
			if bytes, err := client.PublishXML(rssTitle, rssLink, rssDescription, rssAuthor, rssEmail, items); err == nil {
				if _, err := io.Writer.Write(w, bytes); err != nil {
					log.Printf("# failed to write data: %s", err)
				}
			} else {
				log.Printf("# failed to serve RSS feeds: %s", err)
			}
		})

		// listen and serve
		err := http.ListenAndServe(fmt.Sprintf(":%d", httpPort), nil)
		if err != nil {
			log.Printf("# failed to start server: %s", err)
		}
	} else {
		log.Printf("# failed to create a client: %s", err)
	}
}
