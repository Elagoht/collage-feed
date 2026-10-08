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
	"log/slog"
	"net/url"
	"strings"
	"sync"
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
	// absolute. When empty, links follow the origin collage resolves for the
	// request's host: a resolver plugin's (elagoht/tenant), else Config.BaseURL.
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

	drops *dropLog
}

// Item is one entry of a feed.
type Item struct {
	// ID identifies the item for as long as it exists; a reader uses it to tell
	// an item it has seen from a new one. Default: the item's absolute link. An
	// ID that is a URL must be http or https, or a tag: or urn: name; one with
	// another scheme is replaced by the default and logged.
	ID string
	// Title is the item's title, as text.
	Title string
	// Link is the item's page: a path on the site, "/blog/hello", or an absolute
	// http or https URL. A link with any other scheme — javascript:, data:,
	// file: — is left out of the feed and logged.
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
	drops *dropLog
}

// New returns a plugin serving feeds.
func New(feeds ...Feed) *Plugin { return &Plugin{feeds: feeds} }

func (p *Plugin) Name() string                   { return Name }
func (p *Plugin) Version() string                { return "0.2.2" }
func (p *Plugin) Shutdown(context.Context) error { return nil }

// ErrNoBaseURL is returned by Init for a feed with an invalid BaseURL, or with none
// when collage can resolve no origin; and by a render that finds none.
var ErrNoBaseURL = errors.New("feed: BaseURL is required: a feed's links are absolute")

// ErrUnsafeLink is returned by Init for a feed whose Link, RSS or Atom is neither
// a path on the site nor an http or https URL.
var ErrUnsafeLink = errors.New("feed: a link must be a path on the site or an http or https URL")

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
	p.drops = &dropLog{log: host.Logger(), seen: make(map[string]bool)}
	for i := range p.feeds {
		f := &p.feeds[i]
		f.defaults()
		f.drops = p.drops
		// The channel's links are the application's own, and RSS and Atom
		// both require them: one that would be dropped is refused here.
		for _, link := range []string{f.Link, f.RSS, f.Atom} {
			if _, _, ok := checkLink(link); !ok {
				return fmt.Errorf("%w (feed %q)", ErrUnsafeLink, f.Name)
			}
		}
		// The feed's own BaseURL wins; without one, links follow the origin
		// collage resolves for the request's host, read per render.
		if f.BaseURL != "" {
			base, err := url.Parse(f.BaseURL)
			if err != nil || base.Scheme == "" || base.Host == "" {
				return fmt.Errorf("%w (feed %q)", ErrNoBaseURL, f.Name)
			}
		} else if !canResolve(host) {
			return fmt.Errorf("%w (feed %q)", ErrNoBaseURL, f.Name)
		}
		if f.Items == nil {
			return fmt.Errorf("%w (feed %q)", ErrNoItems, f.Name)
		}
		feed := *f
		if f.RSS != "-" {
			doc := collage.NewDocument(docName(f.Name, "rss"), "application/rss+xml; charset=utf-8").
				AtRoot(f.RSS).
				WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
					local, err := feed.at(rc)
					if err != nil {
						return nil, nil, err
					}
					return local.render(ctx, local.rss)
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
				WithHandler(func(ctx context.Context, rc *collage.RenderContext) ([]byte, []string, error) {
					local, err := feed.at(rc)
					if err != nil {
						return nil, nil, err
					}
					return local.render(ctx, local.atom)
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

// at is the feed with its links absolute against rc's origin: its own BaseURL,
// or collage's for the request's host.
func (f Feed) at(rc *collage.RenderContext) (Feed, error) {
	if f.BaseURL == "" {
		f.BaseURL = collage.BaseURL(rc)
		if f.BaseURL == "" {
			return f, fmt.Errorf("%w (feed %q)", ErrNoBaseURL, f.Name)
		}
	}
	return f, nil
}

// canResolve reports whether collage can name an origin without the plugin's
// own BaseURL: from Config.BaseURL, or per host from a plugin implementing
// collage.OriginResolver.
func canResolve(host collage.Host) bool {
	if host.BaseURL() != "" {
		return true
	}
	origins, ok := host.(collage.Origins)
	return ok && origins.Dynamic()
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

// link returns a link as the feed writes it: a path on the site made absolute
// against BaseURL, or an absolute http or https URL. Any other link — a
// javascript:, data: or file: URL, however its scheme is spelled, or one that does
// not parse — is "", and logged once for item.
func (f Feed) link(item, link string) string {
	clean, scheme, ok := checkLink(link)
	if !ok {
		f.drops.warn(f.Name, item, scheme)
		return ""
	}
	if scheme != "" {
		return clean
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return f.BaseURL + clean
}

// id returns the item's id and whether it is its link. An explicit ID is kept
// when it names no scheme, or is a tag: or urn: name or an http or https URL;
// one with any other scheme is logged and replaced by the item's link, which
// is "" when the link was dropped too.
func (f Feed) id(item Item, link string) (string, bool) {
	if item.ID != "" {
		clean := normalise(item.ID)
		switch scheme := schemeOf(clean); scheme {
		case "":
			return item.ID, false
		case "tag", "urn":
			return clean, false
		case "http", "https":
			if abs, _, ok := checkLink(clean); ok {
				return abs, false
			}
			f.drops.warn(f.Name, item.Title, scheme)
		default:
			f.drops.warn(f.Name, item.Title, scheme)
		}
	}
	return link, link != ""
}

// checkLink reports whether link may be written, and how: an absolute http or
// https URL, returned as clean with its scheme, or a reference with no scheme,
// returned as clean with scheme "", for the caller to resolve against BaseURL.
//
// The scheme is found the way a browser finds it, not the way url.Parse does:
// leading and trailing spaces and control characters are trimmed and every
// tab and line break removed first, so "  JavaScript:" and "java\tscript:" are
// javascript: URLs here as they are to the reader that follows them. Anything
// url.Parse refuses is refused too, rather than taken for a path.
func checkLink(link string) (clean, scheme string, ok bool) {
	link = normalise(link)
	scheme = schemeOf(link)
	u, err := url.Parse(link)
	if err != nil {
		return "", scheme, false
	}
	switch scheme {
	case "":
		if u.Scheme != "" || u.Opaque != "" {
			return "", scheme, false
		}
		return link, "", true
	case "http", "https":
		if u.Host == "" || u.Opaque != "" {
			return "", scheme, false
		}
		// As given, not u.String(): that re-encodes a non-ASCII path, and a
		// guid that changes spelling is a new item to every reader. Only the
		// scheme is lower-cased.
		return scheme + link[len(scheme):], scheme, true
	}
	return "", scheme, false
}

// normalise does to a link what a browser's URL parser does before reading it:
// trims leading and trailing C0 controls and spaces, and removes every tab, line
// feed and carriage return.
func normalise(link string) string {
	link = strings.TrimFunc(link, func(r rune) bool { return r <= ' ' })
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, link)
}

// schemeOf returns the scheme a normalised link begins with, lower-cased: a
// letter, then letters, digits, "+", "-" or ".", up to a colon. A link that
// reaches "/", "?", "#" or any other character first has none.
func schemeOf(link string) string {
	for i := 0; i < len(link); i++ {
		c := link[i]
		switch {
		case 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z':
		case i > 0 && ('0' <= c && c <= '9' || c == '+' || c == '-' || c == '.'):
		case i > 0 && c == ':':
			return strings.ToLower(link[:i])
		default:
			return ""
		}
	}
	return ""
}

// dropLog logs a dropped link once per feed, item and scheme, however many times
// and in however many formats the feed is made.
type dropLog struct {
	log  *slog.Logger
	mu   sync.Mutex
	seen map[string]bool
}

// maxDrops bounds what dropLog remembers; past it, it forgets and may log again.
const maxDrops = 4096

func (d *dropLog) warn(feed, item, scheme string) {
	if d == nil || d.log == nil {
		return
	}
	key := feed + "\x00" + item + "\x00" + scheme
	d.mu.Lock()
	if d.seen[key] {
		d.mu.Unlock()
		return
	}
	if len(d.seen) >= maxDrops {
		clear(d.seen)
	}
	d.seen[key] = true
	d.mu.Unlock()
	reason := "only http and https are written"
	if scheme == "" {
		reason = "not a URL"
	}
	// Never the link itself: it is what an attacker wrote.
	d.log.Warn("feed: link dropped", "feed", feed, "item", item, "scheme", scheme, "reason", reason)
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
	Link        string   `xml:"link,omitempty"`
	GUID        *rssGUID `xml:"guid,omitempty"`
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
			Link:        f.link("", f.Link),
			Description: f.Description,
			Language:    f.Language,
			Self:        rssSelf{Href: f.link("", f.RSS), Rel: "self", Type: "application/rss+xml"},
		},
	}
	if t := updated(items); !t.IsZero() {
		doc.Channel.LastBuildDate = t.UTC().Format(time.RFC1123Z)
	}
	for _, item := range items {
		link := f.link(item.Title, item.Link)
		entry := rssItem{
			Title:       item.Title,
			Link:        link,
			Description: item.Summary,
			Creator:     item.Author,
			Categories:  item.Categories,
		}
		if id, permalink := f.id(item, link); id != "" {
			entry.GUID = &rssGUID{Value: id, Permalink: permalink}
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
	Link       *atomLink      `xml:"link,omitempty"`
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
		ID:      f.link("", f.Link),
		Updated: feedUpdated.UTC().Format(time.RFC3339),
		Sub:     f.Description,
		Links: []atomLink{
			{Href: f.link("", f.Atom), Rel: "self", Type: "application/atom+xml"},
			{Href: f.link("", f.Link), Rel: "alternate", Type: "text/html"},
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
		link := f.link(item.Title, item.Link)
		id, _ := f.id(item, link)
		if id == "" {
			// Atom requires an entry's id, and the link it would default to was
			// dropped: the entry is left out. RSS keeps it, without a link.
			continue
		}
		entry := atomEntry{
			Title:   item.Title,
			ID:      id,
			Updated: changed.UTC().Format(time.RFC3339),
			Summary: item.Summary,
		}
		if link != "" {
			entry.Link = &atomLink{Href: link, Rel: "alternate", Type: "text/html"}
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
