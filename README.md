# rss-feeds-go

A go utility package for handling RSS feeds.

## Features

- [X] Fetch RSS feeds from URLs
  - [X] RSS feeds
  - [ ] Atom feeds
  - [X] Manually
  - [ ] Periodically
- [X] Cache fetched RSS feeds locally
  - [X] In memory
  - [X] In SQLite3 file
- [X] Summarize RSS feed's content with Google Gemini API
  - [X] Save summarized contents locally
    - [X] In memory
    - [X] In SQLite3 file
  - [ ] Transfer summarized contents to somewhere
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
    "abcdefghijklmnopqrstuvwxyz0123456789", // google ai api key
    []string{ // rss feeds' urls
      "https://hnrss.org/newest?points=50",
    },
    "./database.sqlite3", // sqlite3 db filepath
  ); err == nil {
    // TODO: do something with `client`
  }
}
```

Other sample applications are in `./samples/` directory.

## License

MIT

