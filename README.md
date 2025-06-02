# rss-feeds-go

A go utility package for handling RSS/Atom feeds.

## Features

- [X] Fetch feeds from URLs
  - [X] RSS feeds (0.90 to 2.0)
  - [X] Atom feeds (0.3, 1.0))
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

## Usages / Samples

Usage:

```go
package main

import (
  "log"

  rf "github.com/meinside/rss-feeds-go"
)

func main() {
  if client, err := rf.NewClientWithDB(
    []string{"abcdefghijklmnopqrstuvwxyz0123456789"}, // google ai api keys (for rotation)
    []string{ // feeds' urls
      "https://hnrss.org/newest?points=50",
      "https://www.hackster.io/news.atom",
    },
    "./database.sqlite3", // sqlite3 db filepath
  ); err == nil {
    // TODO: do something with `client`
  }
}
```

Other sample applications are in `./samples/` directory.

