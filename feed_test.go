package feed_test

import (
	"context"
	"encoding/xml"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	feed "github.com/Elagoht/collage-feed"
	"github.com/Elagoht/collage/pkg/collage"
	"github.com/Elagoht/collage/pkg/collagetest"
)

var published = time.Date(2026, 9, 20, 10, 0, 0, 0, time.UTC)

func items(calls *atomic.Int64, title string) func(context.Context) ([]feed.Item, error) {
	return func(context.Context) ([]feed.Item, error) {
		calls.Add(1)
		return []feed.Item{
			{Title: title, Link: "/blog/hello", Summary: "A <first> post", Content: "<p>Hello &amp; welcome</p>", Author: "Ada",
				Published: published, Updated: published.Add(time.Hour), Categories: []string{"go"}},
			{ID: "urn:post:2", Title: "Second", Link: "https://elsewhere.example/2", Published: published.Add(-24 * time.Hour)},
		}, nil
	}
}

// siteOptions is an application with a cached home page whose head takes the
// feeds' links, plugins, and baseURL as Config.BaseURL.
func siteOptions(baseURL string, plugins ...collage.Plugin) []collagetest.SiteOption {
	return []collagetest.SiteOption{
		collagetest.WithPlugins(plugins...),
		collagetest.WithoutHome(),
		collagetest.WithTemplates(map[string]string{"p.html": `<html><head>{{hoist "head"}}</head><body>x</body></html>`}),
		collagetest.WithRegister(collage.NewPage("home").WithContent(collage.NewFragment("home", "p.html").Build()).WithPath("en", "/").Build()),
		collagetest.WithConfig(func(c *collage.Config) {
			c.BaseURL = baseURL
			// Warming off: an invalidation re-renders a feed in the background, which
			// would race the request that counts the renders after it.
			c.Cache = collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour, WarmLimit: -1}
		}),
	}
}

// site is an application at https://example.com serving feeds.
func site(t *testing.T, feeds ...feed.Feed) *collage.App {
	t.Helper()
	return collagetest.NewSite(t, siteOptions("https://example.com", feed.New(feeds...))...).App
}

func get(app *collage.App, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRSS(t *testing.T) {
	var calls atomic.Int64
	app := site(t, feed.Feed{Title: "Blog", Description: "Posts", Language: "en", Link: "/blog", Items: items(&calls, "Hello"), Tags: []string{"posts"}})
	rec := get(app, "/feed.xml")
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/rss+xml") {
		t.Fatalf("GET /feed.xml = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<rss version="2.0"`,
		`<link>https://example.com/blog</link>`,
		`<atom:link href="https://example.com/feed.xml" rel="self" type="application/rss+xml"></atom:link>`,
		`<guid isPermaLink="true">https://example.com/blog/hello</guid>`,
		`<guid isPermaLink="false">urn:post:2</guid>`,
		`<link>https://elsewhere.example/2</link>`,
		`<pubDate>Sun, 20 Sep 2026 10:00:00 +0000</pubDate>`,
		`<description>A &lt;first&gt; post</description>`,
		`<content:encoded><![CDATA[<p>Hello &amp; welcome</p>]]></content:encoded>`,
		`<dc:creator>Ada</dc:creator>`,
		`<lastBuildDate>Sun, 20 Sep 2026 11:00:00 +0000</lastBuildDate>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("RSS lacks %s\n%s", want, body)
		}
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), new(struct{})); err != nil {
		t.Errorf("RSS is not well-formed XML: %v", err)
	}
}

func TestAtom(t *testing.T) {
	var calls atomic.Int64
	app := site(t, feed.Feed{Title: "Blog", Link: "/blog", Items: items(&calls, "Hello")})
	rec := get(app, "/atom.xml")
	body := rec.Body.String()
	for _, want := range []string{
		`<feed xmlns="http://www.w3.org/2005/Atom">`,
		`<id>https://example.com/blog</id>`,
		`<updated>2026-09-20T11:00:00Z</updated>`,
		`<link href="https://example.com/atom.xml" rel="self" type="application/atom+xml"></link>`,
		`<published>2026-09-20T10:00:00Z</published>`,
		`<author>`,
		`<content type="html">&lt;p&gt;Hello &amp;amp; welcome&lt;/p&gt;</content>`,
		`<category term="go"></category>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("Atom lacks %s\n%s", want, body)
		}
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), new(struct{})); err != nil {
		t.Errorf("Atom is not well-formed XML: %v", err)
	}
}

// A feed is cached until one of its tags is invalidated.
func TestCachedUntilInvalidated(t *testing.T) {
	var calls atomic.Int64
	app := site(t, feed.Feed{Title: "Blog", Items: items(&calls, "Hello"), Tags: []string{"posts"}})
	get(app, "/feed.xml")
	get(app, "/feed.xml")
	if n := calls.Load(); n != 1 {
		t.Errorf("Items called %d times for two requests, want 1", n)
	}
	_ = app.InvalidateTags(context.Background(), "posts")
	get(app, "/feed.xml")
	if n := calls.Load(); n != 2 {
		t.Errorf("Items called %d times after invalidating, want 2", n)
	}
}

// Every page announces the feeds; a feed can stay out of the heads, and a format
// can be left out.
func TestDiscoveryAndFormats(t *testing.T) {
	var calls atomic.Int64
	app := site(t,
		feed.Feed{Name: "blog", Title: `Blog "one"`, Items: items(&calls, "a")},
		feed.Feed{Name: "news", Title: "News", RSS: "/news.xml", Atom: "-", Items: items(&calls, "b")},
		feed.Feed{Name: "quiet", Title: "Quiet", RSS: "/quiet.xml", Atom: "/quiet.atom", NoDiscovery: true, Items: items(&calls, "c")},
	)
	body := get(app, "/").Body.String()
	for _, want := range []string{
		`<link rel="alternate" type="application/rss+xml" title="Blog &#34;one&#34;" href="/feed.xml">`,
		`<link rel="alternate" type="application/atom+xml" title="Blog &#34;one&#34;" href="/atom.xml">`,
		`<link rel="alternate" type="application/rss+xml" title="News" href="/news.xml">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("head lacks %s\n%s", want, body)
		}
	}
	if strings.Contains(body, "quiet") {
		t.Errorf("a NoDiscovery feed is announced:\n%s", body)
	}
	if code := get(app, "/news.xml").Code; code != http.StatusOK {
		t.Errorf("/news.xml = %d", code)
	}
	if code := get(app, "/quiet.atom").Code; code != http.StatusOK {
		t.Errorf("/quiet.atom = %d", code)
	}
}

func TestLimit(t *testing.T) {
	var calls atomic.Int64
	app := site(t, feed.Feed{Title: "Blog", Limit: 1, Items: items(&calls, "only")})
	if n := strings.Count(get(app, "/feed.xml").Body.String(), "<item>"); n != 1 {
		t.Errorf("%d items, want 1", n)
	}
}

// A feed's links are absolute under the application's Config.BaseURL, whatever
// host the request named.
func TestFeed_LinksUnderConfigBaseURL(t *testing.T) {
	var calls atomic.Int64
	site := collagetest.NewSite(t, siteOptions("https://fromconfig.example", feed.New(feed.Feed{Title: "Blog", Link: "/blog", Items: items(&calls, "Hello")}))...)
	body := site.Get("http://other.test/feed.xml").WantStatus(http.StatusOK).Body
	for _, want := range []string{
		`<link>https://fromconfig.example/blog</link>`,
		`<atom:link href="https://fromconfig.example/feed.xml"`,
		`<link>https://fromconfig.example/blog/hello</link>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feed lacks %s, want the app's Config.BaseURL\n%s", want, body)
		}
	}
}

// origins resolves two hosts, as elagoht/tenant would.
type origins struct{}

func (origins) Name() string                             { return "test/origins" }
func (origins) Version() string                          { return "0" }
func (origins) Init(context.Context, collage.Host) error { return nil }
func (origins) Shutdown(context.Context) error           { return nil }
func (origins) Origin(_ context.Context, host string) (string, bool) {
	switch host {
	case "a.test":
		return "https://a.example", true
	case "b.test":
		return "https://b.example", true
	}
	return "", false
}

func oneItem(context.Context) ([]feed.Item, error) {
	return []feed.Item{{Title: "x", Link: "/x"}}, nil
}

// hostSite is an application with no Config.BaseURL, started.
func hostSite(t *testing.T, plugins ...collage.Plugin) *collage.App {
	t.Helper()
	return collagetest.NewSite(t, siteOptions("", plugins...)...).App
}

func TestFeed_OriginFollowsHost(t *testing.T) {
	h := hostSite(t, origins{}, feed.New(feed.Feed{Title: "T", Items: oneItem})).Handler()
	for _, path := range []string{"/feed.xml", "/atom.xml"} {
		for host, want := range map[string]string{"a.test": "https://a.example/x", "b.test": "https://b.example/x"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+path, nil))
			if !strings.Contains(rec.Body.String(), want) {
				t.Errorf("%s %s = %s, want %s", host, path, rec.Body.String(), want)
			}
		}
	}
}

// Without a resolver or a Config.BaseURL, Init fails with ErrNoBaseURL.
func TestFeed_NoOriginIsAnError(t *testing.T) {
	_, err := collagetest.TrySite(t, siteOptions("", feed.New(feed.Feed{Title: "T", Items: oneItem}))...)
	if !errors.Is(err, feed.ErrNoBaseURL) {
		t.Fatalf("Start = %v, want ErrNoBaseURL", err)
	}
}

// With no feeds, as "collage plugin add" writes feed.New(), Init says once where
// they go, and serves nothing.
func TestNoFeedsWarnsOnce(t *testing.T) {
	app, logs := loggedSite(t)
	for range 2 {
		if rec := get(app, "/feed.xml"); rec.Code != http.StatusNotFound {
			t.Errorf("GET /feed.xml = %d, want 404", rec.Code)
		}
	}
	const want = "feed: no feeds; pass them to feed.New in plugins.go"
	var warns []string
	for _, line := range strings.Split(logs.String(), "\n") {
		if strings.Contains(line, "level=WARN") {
			warns = append(warns, line)
		}
	}
	if len(warns) != 1 || !strings.Contains(warns[0], `msg="`+want+`"`) {
		t.Errorf("warnings = %q, want one: %s", warns, want)
	}
}

// A feed silences it.
func TestAFeedDoesNotWarn(t *testing.T) {
	app, logs := loggedSite(t, feed.Feed{Title: "Blog", Items: oneItem})
	get(app, "/feed.xml")
	if strings.Contains(logs.String(), "no feeds") {
		t.Errorf("warned with a feed:\n%s", logs.String())
	}
}
