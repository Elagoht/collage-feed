# elagoht/feed

A collage plugin that serves RSS 2.0 and Atom 1.0 feeds from items the application
lists, and announces them in every page's head.

```go
app, err := collage.New(&collage.Config{
	BaseURL: "https://example.com",
	Plugins: []collage.Plugin{feed.New(feed.Feed{
		Title:       "The blog",
		Description: "What we are working on",
		Language:    "en",
		Link:        "/blog",
		Items:       latestPosts,
		Tags:        []string{"posts"},
	})},
})
```

Requires collage v0.61.0 or later.

`collage plugin add elagoht/feed` writes `feed.New()` into `plugins.go`. Feeds are
Go values, so give them to `feed.New(...)` there; with none, the plugin logs one
warning at startup and serves nothing.

## Links and the origin

A feed's links are absolute. They follow the origin collage resolves for the
request's host: the one a resolver plugin such as elagoht/tenant names for that
host, else the application's `Config.BaseURL` (`BASE_URL`). One feed then serves
each host its own links. With neither, the application fails to start with
`ErrNoBaseURL`.

## Items

`Items` lists what the feed holds, newest first:

```go
func latestPosts(ctx context.Context) ([]feed.Item, error) {
	posts, err := blog.Latest(ctx, 20)
	if err != nil {
		return nil, err
	}
	items := make([]feed.Item, 0, len(posts))
	for _, p := range posts {
		items = append(items, feed.Item{
			Title:      p.Title,
			Link:       "/blog/" + p.Slug, // made absolute under the site's origin
			Summary:    p.Dek,
			Content:    p.HTML, // template.HTML: the full body, optional
			Author:     p.Author,
			Published:  p.PublishedAt,
			Updated:    p.UpdatedAt,
			Categories: p.Tags,
		})
	}
	return items, nil
}
```

An item's `ID` defaults to its absolute link; set it when the link can change. A
feed carries at most `Limit` items, 20 by default.

## Only http and https

A feed reader shows an item's link as something to click, so a link is written only
when it is a path on the site — made absolute against the origin — or an absolute
`http` or `https` URL. Any other scheme, `javascript:`, `data:`, `vbscript:`,
`file:` and the rest, is dropped, however it is spelled: the scheme is read the way
a browser reads it, after trimming spaces and control characters and removing tabs
and line breaks, so `  JavaScript:` and `java<TAB>script:` are caught too. A link
that does not parse as a URL is dropped as well.

This covers every URL the feed writes. An item whose link is dropped stays in the
RSS feed without a `link`, and without a `guid` unless it has an `ID`; Atom requires
an entry's `id`, so such an entry is left out of the Atom feed. An explicit `ID`
that is a URL must be `http` or `https`, or a `tag:` or `urn:` name; one with
another scheme is replaced by the item's link. An `ID` with no scheme, `post-42`,
is kept as it is.

Each dropped link is logged once at `WARN` with the feed, the item's title and the
scheme, never the link itself. A feed's own `Link`, `RSS` or `Atom` that would be
dropped stops the application from starting with `ErrUnsafeLink`: RSS and Atom
both require the channel's link.

## Where the feeds are

Each feed is served at `/feed.xml` as RSS and `/atom.xml` as Atom. `RSS` and `Atom`
move them, and `"-"` leaves a format out. Several feeds need names and their own
paths:

```go
feed.New(
	feed.Feed{Name: "blog", Title: "Blog", RSS: "/blog/feed.xml", Atom: "/blog/atom.xml", …},
	feed.Feed{Name: "news", Title: "News", RSS: "/news.xml", Atom: "-", …},
)
```

Every page announces every feed in its head, where a feed reader looks:

```html
<link rel="alternate" type="application/rss+xml" title="The blog" href="/feed.xml">
```

The layout places it with `{{hoist "head"}}`. `NoDiscovery` keeps a feed out of the
heads.

## Keeping it current

A feed is a static document: cached, exported by a static build, and made again
when one of its `Tags` is invalidated. Invalidate the tag your post pages already
use when a post is published, and the feed follows:

```go
app.InvalidateTags(ctx, "posts")
```

## What is written

- **RSS 2.0** with `atom:link rel="self"`, `guid`, `pubDate`, the summary as
  `description`, the body as `content:encoded` and the author as `dc:creator` —
  RSS's own `author` requires an email address.
- **Atom 1.0** with `id`, `updated`, `published`, `author`, the summary, the body as
  `content type="html"`, and `category`.

Feeds are configured in Go, since `Items` is a function.

## Changes

### v0.3.0

Changed in v0.3.0:

- `Feed.BaseURL` is gone. A feed's links are absolute under collage's own origin:
  set `Config.BaseURL` (`BASE_URL`), or register an origin resolver. A feed that
  set `BaseURL` to something other than the site's origin has no replacement.
- `ErrNoBaseURL` now means only that collage can name no origin.
- Requires collage v0.61.0.

### v0.2.4

- Rebuilt against collage v0.60.0 (`host.MultiOrigin()` replaces the `collage.Origins` assertion). Requires collage v0.60.0.

### v0.2.3

- With no feeds, as `collage plugin add` writes `feed.New()`, `Init` logs one
  `WARN`, `feed: no feeds; pass them to feed.New in plugins.go`, and serves
  nothing.
- collage.json has a `setup` note, which `collage plugin add` prints.
- Built against collage v0.58.0. Requires it.

### v0.2.2

- An absolute `http` or `https` link or `ID` is written as given again. v0.2.1
  re-encoded it, so a non-ASCII path such as `/blog/çay` became `/blog/%C3%A7ay`,
  and a reader would show the item as new. Only the scheme's case is changed.

### v0.2.1

- **Links are written only as `http` or `https`.** An item link, a URL-shaped
  `ID`, the channel link and the `atom:link` with any other scheme —
  `javascript:`, `data:`, `vbscript:`, `file:`, in any case and with spaces,
  tabs or line breaks worked in — are dropped and logged once at `WARN` with the
  scheme, not the link. Before, an absolute link of any scheme was written as it
  was. A feed's own `Link`, `RSS` or `Atom` with such a scheme stops the
  application from starting (`ErrUnsafeLink`). See
  [Only http and https](#only-http-and-https).
- Requires collage v0.55.0.
