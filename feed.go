package rf

import (
	"encoding/xml"
)

// https://github.com/gorilla/feeds/blob/main/rss.go

// Feeds struct
type Feeds struct {
	XMLName          xml.Name `xml:"rss"`
	Version          string   `xml:"version,attr"`
	ContentNamespace string   `xml:"xmlns:content,attr"`

	Channel Channel `xml:"channel"`
}

// Channel struct
type Channel struct {
	XMLName xml.Name `xml:"channel"`

	Title       string `xml:"title"`
	Link        string `xml:"link"`
	Description string `xml:"description"`
	Items       []Item `xml:"item"`
}

// Item struct
type Item struct {
	XMLName xml.Name `xml:"item"`

	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        GUID
	Author      string `xml:"author"`
	PublishDate string `xml:"pubDate"`
	Comments    string `xml:"comments"`
	Description string `xml:"description"`
	Content     Content
	Categories  string `xml:"category"`
}

// GUID struct
type GUID struct {
	XMLName xml.Name `xml:"guid"`

	ID          string `xml:",chardata"`
	IsPermaLink string `xml:"isPermaLink,attr,omitempty"`
}

// Content struct
type Content struct {
	XMLName xml.Name `xml:"content:encoded"`
	Content string   `xml:",cdata"`
}
