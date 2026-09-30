package feed_test

import (
	"context"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	feed "github.com/Elagoht/collage-feed"
	"github.com/Elagoht/collage/pkg/collage"
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

func site(t *testing.T, feeds ...feed.Feed) *collage.App {
	t.Helper()
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head>{{hoist "head"}}</head><body>x</body></html>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Plugins: []collage.Plugin{feed.New(feeds...)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(collage.NewPage("home").WithContent(collage.NewFragment("home", "p.html").Build()).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	return app
}

func get(app *collage.App, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestRSS(t *testing.T) {
	var calls atomic.Int64
	app := site(t, feed.Feed{Title: "Blog", Description: "Posts", Language: "en", BaseURL: "https://example.com/", Link: "/blog", Items: items(&calls, "Hello"), Tags: []string{"posts"}})
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
	app := site(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Link: "/blog", Items: items(&calls, "Hello")})
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
	app := site(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Items: items(&calls, "Hello"), Tags: []string{"posts"}})
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
		feed.Feed{Name: "blog", Title: `Blog "one"`, BaseURL: "https://example.com", Items: items(&calls, "a")},
		feed.Feed{Name: "news", Title: "News", BaseURL: "https://example.com", RSS: "/news.xml", Atom: "-", Items: items(&calls, "b")},
		feed.Feed{Name: "quiet", Title: "Quiet", BaseURL: "https://example.com", RSS: "/quiet.xml", Atom: "/quiet.atom", NoDiscovery: true, Items: items(&calls, "c")},
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
	app := site(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Limit: 1, Items: items(&calls, "only")})
	if n := strings.Count(get(app, "/feed.xml").Body.String(), "<item>"); n != 1 {
		t.Errorf("%d items, want 1", n)
	}
}

// A feed with no BaseURL of its own falls back to the application's Config.BaseURL.
func TestFeed_FallsBackToConfigBaseURL(t *testing.T) {
	var calls atomic.Int64
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head>{{hoist "head"}}</head><body>x</body></html>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		BaseURL: "https://fromconfig.example",
		Plugins: []collage.Plugin{feed.New(feed.Feed{Title: "Blog", Link: "/blog", Items: items(&calls, "Hello")})}, // no BaseURL of its own
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.RegisterPage(collage.NewPage("home").WithContent(collage.NewFragment("home", "p.html").Build()).WithPath("en", "/").Build()); err != nil {
		t.Fatal(err)
	}
	rec := get(app, "/feed.xml")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /feed.xml = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<link>https://fromconfig.example/blog</link>`,
		`<atom:link href="https://fromconfig.example/feed.xml"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("feed lacks %s, want the app's Config.BaseURL\n%s", want, body)
		}
	}
}
