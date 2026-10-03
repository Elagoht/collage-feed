# elagoht/feed

A collage plugin that serves RSS 2.0 and Atom 1.0 feeds from items the application
lists, and announces them in every page's head.

```go
app, err := collage.New(&collage.Config{
	Plugins: []collage.Plugin{feed.New(feed.Feed{
		Title:       "The blog",
		Description: "What we are working on",
		Language:    "en",
		BaseURL:     "https://example.com",
		Link:        "/blog",
		Items:       latestPosts,
		Tags:        []string{"posts"},
	})},
})
```

Requires collage v0.42.0 or later.

## Links and the origin

A feed's links are absolute. `BaseURL` is the feed's own origin and always wins.
Without one, links follow the origin collage resolves for the request's host: the
one a resolver plugin such as elagoht/tenant names for that host, else the
application's `Config.BaseURL`. One feed then serves each host its own links. With
neither, the application fails to start with `ErrNoBaseURL`.

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
			Link:       "/blog/" + p.Slug, // made absolute with BaseURL
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
