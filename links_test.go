package feed_test

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	feed "github.com/Elagoht/collage-feed"
	"github.com/Elagoht/collage/pkg/collage"
)

// syncBuffer is a log destination safe for concurrent writes.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loggedSite is site() with the application's log written to the returned buffer.
func loggedSite(t *testing.T, feeds ...feed.Feed) (*collage.App, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	app, err := collage.New(&collage.Config{
		Server: collage.ServerConfig{Host: "localhost", Port: 3000},
		Template: collage.TemplateConfig{FS: fstest.MapFS{
			"t/p.html": {Data: []byte(`<html><head>{{hoist "head"}}</head><body>x</body></html>`)},
		}, Root: "t"},
		Cache:   collage.CacheConfig{Enabled: true, Type: "memory", DefaultTTL: time.Hour},
		Logger:  slog.New(slog.NewTextHandler(logs, nil)),
		Plugins: []collage.Plugin{feed.New(feeds...)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, logs
}

// dangerous are links a reader must never be handed, each carrying PAYLOAD so
// its survival anywhere in a feed or the log is visible. Some url.Parse refuses
// and some it accepts; neither may get through.
var dangerous = []struct{ link, scheme string }{
	{"javascript:PAYLOAD()", "javascript"},
	{"JavaScript:PAYLOAD()", "javascript"},
	{"JAVASCRIPT:PAYLOAD()", "javascript"},
	{"  javascript:PAYLOAD()", "javascript"},
	{"\x01\x1fjavascript:PAYLOAD()", "javascript"},
	{"javascript:PAYLOAD()  ", "javascript"},
	{"java\tscript:PAYLOAD()", "javascript"},
	{"java\nscript:PAYLOAD()", "javascript"},
	{"java\r\nscript:PAYLOAD()", "javascript"},
	{"\tjavascript\t:PAYLOAD()", "javascript"},
	{"javascript://example.com/%0aPAYLOAD()", "javascript"},
	{"data:text/html,<script>PAYLOAD()</script>", "data"},
	{"DATA:text/html;base64,PAYLOAD", "data"},
	{"vbscript:PAYLOAD", "vbscript"},
	{"file:///etc/PAYLOAD", "file"},
	{"mailto:PAYLOAD@example.com", "mailto"},
	{"x-custom+app.1:PAYLOAD", "x-custom+app.1"},
	{"http:PAYLOAD", "http"},
	{"https:///PAYLOAD", "https"},
	{"http://exa mple.com/PAYLOAD", "http"},
	{"/blog/\x7fPAYLOAD", ""},
}

func dangerousItems(context.Context) ([]feed.Item, error) {
	items := []feed.Item{
		{Title: "relative", Link: "/blog/ok", Published: published},
		{Title: "absolute", Link: "HTTPS://ok.example/a", Published: published},
		{Title: "tagged", ID: "tag:example.com,2026:1", Link: "/blog/tagged", Published: published},
		{Title: "urn", ID: "urn:uuid:6e8bc430-9c3a-11d9-9669-0800200c9a66", Link: "/blog/urn", Published: published},
		{Title: "plain id", ID: "post-42", Link: "/blog/42", Published: published},
	}
	for i, d := range dangerous {
		items = append(items, feed.Item{Title: "link " + string(rune('a'+i)), Link: d.link, Published: published})
	}
	items = append(items,
		feed.Item{Title: "bad id", ID: "javascript:PAYLOAD()", Link: "/blog/badid", Published: published},
		feed.Item{Title: "bad id only", ID: "data:,PAYLOAD", Published: published},
	)
	return items, nil
}

// written collects every URL-valued thing a feed wrote: links, hrefs, guids, ids.
func written(t *testing.T, body []byte) []string {
	t.Helper()
	var out []string
	dec := xml.NewDecoder(strings.NewReader(string(body)))
	inURL := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tok := tok.(type) {
		case xml.StartElement:
			inURL = tok.Name.Local == "link" || tok.Name.Local == "guid" || tok.Name.Local == "id"
			for _, a := range tok.Attr {
				if a.Name.Local == "href" {
					out = append(out, a.Value)
				}
			}
		case xml.CharData:
			if inURL && strings.TrimSpace(string(tok)) != "" {
				out = append(out, string(tok))
			}
		case xml.EndElement:
			inURL = false
		}
	}
	return out
}

// TestDangerousLinksAreDropped: only http and https links reach a feed, however
// the scheme is spelled; relative links resolve against BaseURL; an explicit ID
// may be a tag: or urn: name or a plain string, but not another scheme.
func TestDangerousLinksAreDropped(t *testing.T) {
	app, logs := loggedSite(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Link: "/blog", Items: dangerousItems, Limit: 100})
	for _, path := range []string{"/feed.xml", "/atom.xml", "/feed.xml"} {
		rec := get(app, path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		body := rec.Body.Bytes()
		if err := xml.Unmarshal(body, new(struct{})); err != nil {
			t.Errorf("%s is not well-formed XML: %v", path, err)
		}
		if strings.Contains(strings.ToLower(string(body)), "payload") {
			t.Errorf("%s carries a dangerous link:\n%s", path, body)
		}
		for _, u := range written(t, body) {
			if !strings.HasPrefix(u, "https://example.com/") && !strings.HasPrefix(u, "https://ok.example/") &&
				!strings.HasPrefix(u, "tag:") && !strings.HasPrefix(u, "urn:") && u != "post-42" {
				t.Errorf("%s writes %q", path, u)
			}
		}
		for _, want := range []string{
			"https://example.com/blog/ok", "https://ok.example/a", "tag:example.com,2026:1",
			"urn:uuid:6e8bc430-9c3a-11d9-9669-0800200c9a66", "post-42", "https://example.com/blog/badid",
		} {
			if !strings.Contains(string(body), want) {
				t.Errorf("%s lacks %s", path, want)
			}
		}
	}

	log := logs.String()
	if strings.Contains(strings.ToLower(log), "payload") {
		t.Errorf("the log carries a dangerous link:\n%s", log)
	}
	warns := 0
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "feed:") {
			warns++
		}
	}
	// One per dangerous item, however many times and formats it was written in.
	if want := len(dangerous) + 2; warns != want {
		t.Errorf("%d warnings, want %d:\n%s", warns, want, log)
	}
	for _, d := range dangerous {
		if d.scheme != "" && !strings.Contains(log, "scheme="+d.scheme) {
			t.Errorf("no warning names scheme %q", d.scheme)
		}
	}
	if !strings.Contains(log, `item="link a"`) {
		t.Errorf("the warning does not name the item:\n%s", log)
	}
}

// An item whose link was dropped is still in the RSS feed, without a link or a
// guid; Atom requires an entry's id, so there it is left out.
func TestItemWithoutLinkOrID(t *testing.T) {
	app, _ := loggedSite(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Items: func(context.Context) ([]feed.Item, error) {
		return []feed.Item{{Title: "lost", Link: "javascript:PAYLOAD()"}, {Title: "kept", Link: "/kept"}}, nil
	}})
	rss := get(app, "/feed.xml").Body.String()
	if !strings.Contains(rss, "<title>lost</title>") || !strings.Contains(rss, "<title>kept</title>") {
		t.Errorf("RSS lost an item:\n%s", rss)
	}
	if strings.Count(rss, "<guid") != 1 || strings.Count(rss, "<link>") != 2 { // the channel's and kept's
		t.Errorf("RSS writes a link or guid for the item without one:\n%s", rss)
	}
	atom := get(app, "/atom.xml").Body.String()
	if strings.Contains(atom, "<title>lost</title>") || !strings.Contains(atom, "<title>kept</title>") {
		t.Errorf("Atom kept the entry without an id, or lost the other:\n%s", atom)
	}
}

// A channel link, or a path a format is served at, that is not http, https or a
// path on the site stops the application from starting.
func TestUnsafeChannelLinkIsRefused(t *testing.T) {
	for _, f := range []feed.Feed{
		{Title: "T", BaseURL: "https://example.com", Link: "javascript:PAYLOAD()", Items: oneItem},
		{Title: "T", BaseURL: "https://example.com", Link: " JavaScript:PAYLOAD()", Items: oneItem},
		{Title: "T", BaseURL: "https://example.com", Link: "data:,PAYLOAD", Items: oneItem},
	} {
		app := hostSite(t, feed.New(f))
		if err := app.Start(); !errors.Is(err, feed.ErrUnsafeLink) {
			t.Errorf("Link %q: Start = %v, want ErrUnsafeLink", f.Link, err)
		}
	}
	// An absolute http(s) channel link is still allowed.
	app, _ := loggedSite(t, feed.Feed{Title: "T", BaseURL: "https://example.com", Link: "https://www.example.com/blog", Items: oneItem})
	if body := get(app, "/feed.xml").Body.String(); !strings.Contains(body, "<link>https://www.example.com/blog</link>") {
		t.Errorf("the absolute channel link was not kept:\n%s", body)
	}
}

// An absolute link or ID is written as given, not re-encoded: a guid that changes
// spelling makes every reader show the item again as new.
func TestAbsoluteLinksAreWrittenAsGiven(t *testing.T) {
	app, _ := loggedSite(t, feed.Feed{Title: "Blog", BaseURL: "https://example.com", Items: func(context.Context) ([]feed.Item, error) {
		return []feed.Item{
			{Title: "çay", Link: "https://tr.example/blog/çay-demlemek"},
			{Title: "ğ", ID: "https://tr.example/id/ğüşiöç", Link: "/x"},
		}, nil
	}})
	for _, path := range []string{"/feed.xml", "/atom.xml"} {
		body := get(app, path).Body.String()
		for _, want := range []string{"https://tr.example/blog/çay-demlemek", "https://tr.example/id/ğüşiöç"} {
			if !strings.Contains(body, want) {
				t.Errorf("%s lacks %s as given:\n%s", path, want, body)
			}
		}
	}
}
