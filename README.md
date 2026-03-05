# rss-feeds-go

A go utility package for handling RSS/Atom feeds.

## Features

- [X] Fetch feeds from URLs
  - [X] RSS feeds (0.90 to 2.0)
  - [X] Atom feeds (0.3, 1.0)
  - [X] JSON feeds (1.0, 1.1)
  - [X] Manually
  - [ ] Periodically
- [X] Cache fetched feeds locally
  - [X] In memory
  - [X] In SQLite3 file
- [X] Summarize contents of fetched feed items with Google Gemini API
  - [X] Save summarized contents locally
    - [X] In memory
    - [X] In SQLite3 file
  - [ ] Transfer summarized contents to somewhere else
- [X] Convert cached feeds as RSS XML

## Installation

```bash
$ go get github.com/meinside/rss-feeds-go
```

## Usage

```go
package main

import (
  "context"
  "log"
  "time"

  rf "github.com/meinside/rss-feeds-go"
)

func main() {
  client, err := rf.NewClientWithDB(
    []string{"your-google-ai-api-key"}, // google ai api keys (for rotation)
    []string{ // feeds' urls
      "https://hnrss.org/newest?points=50",
      "https://www.hackster.io/news.atom",
    },
    "./database.sqlite3", // sqlite3 db filepath
  )
  if err != nil {
    log.Fatalf("failed to create client: %s", err)
  }

  // (optional) configure client
  client.SetGoogleAIModels([]string{"gemini-2.5-flash"})
  client.SetDesiredLanguage("Korean")
  client.SetVerbose(true)

  // fetch feeds
  ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
  defer cancel()

  feeds, err := client.FetchFeeds(ctx, true, 7) // ignore cached, ignore older than 7 days
  if err != nil {
    log.Printf("fetch error: %s", err)
  }

  // summarize and cache
  if err := client.SummarizeAndCacheFeeds(feeds); err != nil {
    log.Printf("summarize error: %s", err)
  }

  // list cached items
  items := client.ListCachedItems(false) // unread only

  // publish as RSS XML
  bytes, err := client.PublishXML(
    "My Feed", "https://example.com", "My summarized feeds",
    "author", "email@example.com",
    items,
  )
  if err != nil {
    log.Fatalf("failed to publish: %s", err)
  }

  log.Printf("RSS XML: %s", string(bytes))

  // mark as read and cleanup
  client.MarkCachedItemsAsRead(items)
  client.DeleteOldCachedItems()
}
```

Other sample applications are in the `./samples/` directory.
