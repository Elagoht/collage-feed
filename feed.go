// Package feed is a collage plugin that serves RSS 2.0 and Atom 1.0 feeds.
//
//	app, err := collage.New(&collage.Config{
//		Plugins: []collage.Plugin{feed.New(feed.Feed{
//			Name:    "blog",
//			Title:   "The blog",
//			BaseURL: "https://example.com",
//			Link:    "/blog",
//			Items:   latestPosts,
//			Tags:    []string{"posts"},
//		})},
//	})
//
// Each feed is served at /feed.xml as RSS and /atom.xml as Atom unless it says
// otherwise, and announced in every page's head with <link rel="alternate">, so a
// reader pointed at any page finds it. A feed is a static document: cached,
// exported by a static build, and made again when one of its Tags is invalidated.
package feed

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html/template"
	"net/url"
	"strings"
	"time"

	"github.com/Elagoht/collage/pkg/collage"
)

// Name is the plugin's name.
const Name = "elagoht/feed"

// Feed describes one feed.
type Feed struct {
	// Name tells feeds apart; it is part of their documents' names. Required
	// when there is more than one.
	Name string
	// Title and Description describe the feed to a reader's feed list.
	Title       string
	Description string
	// Language is the feed's language, as a BCP 47 tag: "en", "tr".
	Language string
	// BaseURL is the site's origin, "https://example.com": a feed's links are
	// absolute, and the application cannot know its own host. Required.
	BaseURL string
	// Link is the path of the page the feed is the feed of: "/blog". Default "/".
	Link string
	// RSS and Atom are where the two formats are served. Default "/feed.xml" and
	// "/atom.xml"; "-" leaves a format out.
	RSS  string
	Atom string
	// Items lists what the feed holds, newest first. Called when the feed is
	// rendered — once until one of Tags is invalidated.
	Items func(ctx context.Context) ([]Item, error)
	// Tags are the dependency tags the feed is made from: invalidating one makes
	// the feed again.
	Tags []string
	// Limit caps how many items the feed carries. Default 20.
	Limit int
	// NoDiscovery leaves the feed out of the pages' heads.
	NoDiscovery bool
}

// Item is one entry of a feed.
type Item struct {
	// ID identifies the item for as long as it exists; a reader uses it to tell
	// an item it has seen from a new one. Default: the item's absolute link.
	ID string
	// Title is the item's title, as text.
	Title string
	// Link is the item's page: a path on the site, "/blog/hello", or an absolute
	// URL.
	Link string
	// Summary is a short description, as text.
	Summary string
	// Content is the item's full body, as HTML. Optional.
	Content template.HTML
	// Author is the author's name.
	Author string
	// Published is when the item was first published; Updated when it last
	// changed, or zero when it never did.
	Published time.Time
	Updated   time.Time
	// Categories are the item's tags or topics.
	Categories []string
}

// Plugin serves the feeds.
type Plugin struct {
	feeds []Feed
}

// New returns a plugin serving feeds.
func New(feeds ...Feed) *Plugin { return &Plugin{feeds: feeds} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.1.1" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

// ErrNoBaseURL is returned by Init for a feed without an absolute BaseURL.
var ErrNoBaseURL = errors.New("feed: BaseURL is required: a feed's links are absolute")

// ErrNoItems is returned by Init for a feed without an Items function.
var ErrNoItems = errors.New("feed: Items is required")

// Init registers each feed's documents.
func (p *Plugin) Init(_ context.Context, host collage.Host) error {
	if len(p.feeds) > 1 {
		seen := make(map[string]bool)
		for _, f := range p.feeds {
			if f.Name == "" || seen[f.Name] {
				return fmt.Errorf("feed: several feeds need distinct names, got %q twice or empty", f.Name)
			}
			seen[f.Name] = true
		}
	}
	for i := range p.feeds {
		f := &p.feeds[i]
		f.defaults()
		base, err := url.Parse(f.BaseURL)
		if f.BaseURL == "" || err != nil || base.Scheme == "" || base.Host == "" {
			return fmt.Errorf("%w (feed %q)", ErrNoBaseURL, f.Name)
		}
		if f.Items == nil {
			return fmt.Errorf("%w (feed %q)", ErrNoItems, f.Name)
		}
		feed := *f
		if f.RSS != "-" {
			doc := collage.NewDocument(docName(f.Name, "rss"), "application/rss+xml; charset=utf-8").
				AtRoot(f.RSS).
				WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
					return feed.render(ctx, feed.rss)
				}).
				Static().
				Build()
			if err := host.RegisterDocument(doc); err != nil {
				return fmt.Errorf("feed: %w", err)
			}
		}
		if f.Atom != "-" {
			doc := collage.NewDocument(docName(f.Name, "atom"), "application/atom+xml; charset=utf-8").
				AtRoot(f.Atom).
				WithHandler(func(ctx context.Context, _ *collage.RenderContext) ([]byte, []string, error) {
					return feed.render(ctx, feed.atom)
				}).
				Static().
				Build()
			if err := host.RegisterDocument(doc); err != nil {
				return fmt.Errorf("feed: %w", err)
			}
		}
	}
	return nil
}

func docName(feed, format string) string {
	if feed == "" {
		return Name + ":" + format
	}
	return Name + ":" + feed + ":" + format
}

func (f *Feed) defaults() {
	f.BaseURL = strings.TrimSuffix(f.BaseURL, "/")
	if f.Link == "" {
		f.Link = "/"
	}
	if f.RSS == "" {
		f.RSS = "/feed.xml"
	}
	if f.Atom == "" {
		f.Atom = "/atom.xml"
	}
	if f.Limit <= 0 {
		f.Limit = 20
	}
}

// OnBeforeRender announces the feeds in the page's head, where a feed reader
// looks for them. Hoisted at depth zero, so a page can replace one by declaring
// the same key.
func (p *Plugin) OnBeforeRender(_ context.Context, ev *collage.BeforeRenderEvent) error {
	if ev.Context == nil {
		return nil
	}
	for _, f := range p.feeds {
		if f.NoDiscovery {
			continue
		}
		title := template.HTMLEscapeString(f.Title)
		if f.RSS != "-" {
			ev.Context.Hoist("head", "feed:"+f.Name+":rss", template.HTML( // assembled here from escaped values
				`<link rel="alternate" type="application/rss+xml" title="`+title+`" href="`+template.HTMLEscapeString(f.RSS)+`">`))
		}
		if f.Atom != "-" {
			ev.Context.Hoist("head", "feed:"+f.Name+":atom", template.HTML( // assembled here from escaped values
				`<link rel="alternate" type="application/atom+xml" title="`+title+`" href="`+template.HTMLEscapeString(f.Atom)+`">`))
		}
	}
	return nil
}

func (f Feed) render(ctx context.Context, format func([]Item) ([]byte, error)) ([]byte, []string, error) {
	items, err := f.Items(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(items) > f.Limit {
		items = items[:f.Limit]
	}
	body, err := format(items)
	return body, f.Tags, err
}

// absolute makes a link on the site absolute; an absolute one is kept.
func (f Feed) absolute(link string) string {
	if u, err := url.Parse(link); err == nil && u.IsAbs() {
		return link
	}
	if !strings.HasPrefix(link, "/") {
		link = "/" + link
	}
	return f.BaseURL + link
}

func (f Feed) id(item Item) string {
	if item.ID != "" {
		return item.ID
	}
	return f.absolute(item.Link)
}

// updated is when the feed last changed: its newest item's time.
func updated(items []Item) time.Time {
	var latest time.Time
	for _, item := range items {
		t := item.Updated
		if t.IsZero() {
			t = item.Published
		}
		if t.After(latest) {
			latest = t
		}
	}
	return latest
}

// --- RSS 2.0 ---------------------------------------------------------------------

type rss struct {
	XMLName xml.Name   `xml:"rss"`
	Version string     `xml:"version,attr"`
	Atom    string     `xml:"xmlns:atom,attr"`
	Content string     `xml:"xmlns:content,attr"`
	DC      string     `xml:"xmlns:dc,attr"`
	Channel rssChannel `xml:"channel"`
}

type rssChannel struct {
	Title         string    `xml:"title"`
	Link          string    `xml:"link"`
	Description   string    `xml:"description"`
	Language      string    `xml:"language,omitempty"`
	LastBuildDate string    `xml:"lastBuildDate,omitempty"`
	Self          rssSelf   `xml:"atom:link"`
	Items         []rssItem `xml:"item"`
}

type rssSelf struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
	Type string `xml:"type,attr"`
}

type rssItem struct {
	Title       string   `xml:"title"`
	Link        string   `xml:"link"`
	GUID        rssGUID  `xml:"guid"`
	PubDate     string   `xml:"pubDate,omitempty"`
	Description string   `xml:"description,omitempty"`
	Content     *cdata   `xml:"content:encoded,omitempty"`
	Creator     string   `xml:"dc:creator,omitempty"`
	Categories  []string `xml:"category"`
}

type rssGUID struct {
	Value     string `xml:",chardata"`
	Permalink bool   `xml:"isPermaLink,attr"`
}

type cdata struct {
	Value string `xml:",cdata"`
}

func (f Feed) rss(items []Item) ([]byte, error) {
	doc := rss{
		Version: "2.0",
		Atom:    "http://www.w3.org/2005/Atom",
		Content: "http://purl.org/rss/1.0/modules/content/",
		DC:      "http://purl.org/dc/elements/1.1/",
		Channel: rssChannel{
			Title:       f.Title,
			Link:        f.absolute(f.Link),
			Description: f.Description,
			Language:    f.Language,
			Self:        rssSelf{Href: f.absolute(f.RSS), Rel: "self", Type: "application/rss+xml"},
		},
	}
	if t := updated(items); !t.IsZero() {
		doc.Channel.LastBuildDate = t.UTC().Format(time.RFC1123Z)
	}
	for _, item := range items {
		entry := rssItem{
			Title:       item.Title,
			Link:        f.absolute(item.Link),
			GUID:        rssGUID{Value: f.id(item), Permalink: item.ID == ""},
			Description: item.Summary,
			Creator:     item.Author,
			Categories:  item.Categories,
		}
		if !item.Published.IsZero() {
			entry.PubDate = item.Published.UTC().Format(time.RFC1123Z)
		}
		if item.Content != "" {
			entry.Content = &cdata{Value: string(item.Content)}
		}
		doc.Channel.Items = append(doc.Channel.Items, entry)
	}
	return marshal(doc)
}

// --- Atom 1.0 --------------------------------------------------------------------

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Lang    string      `xml:"xml:lang,attr,omitempty"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Links   []atomLink  `xml:"link"`
	Sub     string      `xml:"subtitle,omitempty"`
	Entries []atomEntry `xml:"entry"`
}

type atomLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
}

type atomEntry struct {
	Title      string         `xml:"title"`
	ID         string         `xml:"id"`
	Link       atomLink       `xml:"link"`
	Updated    string         `xml:"updated"`
	Published  string         `xml:"published,omitempty"`
	Author     *atomAuthor    `xml:"author,omitempty"`
	Summary    string         `xml:"summary,omitempty"`
	Content    *atomContent   `xml:"content,omitempty"`
	Categories []atomCategory `xml:"category"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomContent struct {
	Type  string `xml:"type,attr"`
	Value string `xml:",chardata"`
}

type atomCategory struct {
	Term string `xml:"term,attr"`
}

func (f Feed) atom(items []Item) ([]byte, error) {
	feedUpdated := updated(items)
	if feedUpdated.IsZero() {
		// Atom requires one; a feed with nothing in it has not changed since the
		// epoch as far as anyone can tell.
		feedUpdated = time.Unix(0, 0)
	}
	doc := atomFeed{
		Lang:    f.Language,
		Title:   f.Title,
		ID:      f.absolute(f.Link),
		Updated: feedUpdated.UTC().Format(time.RFC3339),
		Sub:     f.Description,
		Links: []atomLink{
			{Href: f.absolute(f.Atom), Rel: "self", Type: "application/atom+xml"},
			{Href: f.absolute(f.Link), Rel: "alternate", Type: "text/html"},
		},
	}
	for _, item := range items {
		changed := item.Updated
		if changed.IsZero() {
			changed = item.Published
		}
		if changed.IsZero() {
			changed = feedUpdated
		}
		entry := atomEntry{
			Title:   item.Title,
			ID:      f.id(item),
			Link:    atomLink{Href: f.absolute(item.Link), Rel: "alternate", Type: "text/html"},
			Updated: changed.UTC().Format(time.RFC3339),
			Summary: item.Summary,
		}
		if !item.Published.IsZero() {
			entry.Published = item.Published.UTC().Format(time.RFC3339)
		}
		if item.Author != "" {
			entry.Author = &atomAuthor{Name: item.Author}
		}
		if item.Content != "" {
			entry.Content = &atomContent{Type: "html", Value: string(item.Content)}
		}
		for _, c := range item.Categories {
			entry.Categories = append(entry.Categories, atomCategory{Term: c})
		}
		doc.Entries = append(doc.Entries, entry)
	}
	return marshal(doc)
}

func marshal(doc any) ([]byte, error) { // any: encoding/xml's own parameter type
	var b bytes.Buffer
	b.WriteString(xml.Header)
	enc := xml.NewEncoder(&b)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	b.WriteByte('\n')
	return b.Bytes(), nil
}
