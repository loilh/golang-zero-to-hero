# Chapter 6: Concurrent Web Scraper — `goscrape`

The previous chapter built a CLI tool that taught you Cobra, file persistence, and cross-platform builds. This chapter teaches something different: how to design a **concurrent pipeline** that is fast, polite, resumable, and handles failure gracefully.

We'll scrape Hacker News — a simple, well-structured public site with no JavaScript rendering required. The architecture applies equally to any news aggregator or crawl job.

---

## Architecture overview

The scraper is a classic **fan-out / fan-in pipeline**:

```
┌──────────────┐    ┌───────────────────────────────┐    ┌─────────────┐
│  Seed URLs   │──▶ │  URL Queue (buffered channel) │──▶ │  Worker 1   │─┐
└──────────────┘    └───────────────────────────────┘    └─────────────┘ │
                                                          ┌─────────────┐ │  Results
                                                          │  Worker 2   │─┤──chan──▶ Aggregator ──▶ Export
                                                          └─────────────┘ │
                                                          ┌─────────────┐ │
                                                          │  Worker N   │─┘
                                                          └─────────────┘
                                                                │
                                                          Error channel
```

Each worker:
1. Reads a URL from the queue channel.
2. Checks the checkpoint file — skip if already scraped.
3. Waits for a token from the rate limiter.
4. Fetches the URL with the HTTP client.
5. Parses the HTML using goquery.
6. Sends the result (or error) to the result/error channel.

The aggregator drains the results channel, writes JSON/CSV, and updates the progress bar.

---

## Project structure

```
goscrape/
├── cmd/
│   ├── root.go
│   └── scrape.go
├── internal/
│   ├── scraper/
│   │   ├── scraper.go
│   │   ├── worker.go
│   │   └── parser.go
│   ├── ratelimit/
│   │   └── limiter.go
│   ├── export/
│   │   ├── json.go
│   │   └── csv.go
│   └── checkpoint/
│       └── checkpoint.go
├── main.go
└── go.mod
```

---

## Bootstrapping

```bash
mkdir goscrape && cd goscrape
go mod init github.com/yourname/goscrape
go get github.com/PuerkitoBio/goquery@v1.9.0
go get github.com/schollz/progressbar/v3@v3.14.0
go get github.com/spf13/cobra@v1.8.0
```

---

## go.mod

```go
module github.com/yourname/goscrape

go 1.21

require (
	github.com/PuerkitoBio/goquery v1.9.0
	github.com/schollz/progressbar/v3 v3.14.0
	github.com/spf13/cobra v1.8.0
)
```

---

## The data model

Before writing any pipeline code, define what data flows through it. Everything is a `Story`:

```go
// internal/scraper/scraper.go  (model section at top of file)

// Story represents one item scraped from the HN front page.
type Story struct {
	Rank        int       `json:"rank"`
	Title       string    `json:"title"`
	URL         string    `json:"url"`
	Points      int       `json:"points"`
	Author      string    `json:"author"`
	CommentURL  string    `json:"comment_url"`
	NumComments int       `json:"num_comments"`
	ScrapedAt   time.Time `json:"scraped_at"`
}

// ScrapeResult bundles a story with the source URL it came from.
// The pipeline passes these through channels, not pointers, to avoid
// data races — each goroutine has its own copy.
type ScrapeResult struct {
	Story   Story
	PageURL string
}

// ScrapeError bundles an error with the URL that produced it so the
// aggregator can log them without stopping the whole pipeline.
type ScrapeError struct {
	URL string
	Err error
}
```

---

## The checkpoint — `internal/checkpoint/checkpoint.go`

The checkpoint lets you resume a scrape after it's interrupted. We store seen URLs in a JSON file. The design favors simplicity — for very large crawls you'd use a SQLite database, but for HN's ~30 pages a JSON file is fine.

```go
// internal/checkpoint/checkpoint.go
package checkpoint

import (
	"encoding/json"
	"os"
	"sync"
)

// Checkpoint tracks which URLs have been successfully scraped.
// It is safe to use from multiple goroutines.
type Checkpoint struct {
	mu      sync.RWMutex
	path    string
	seen    map[string]bool
}

// New loads an existing checkpoint file or creates an empty one.
func New(path string) (*Checkpoint, error) {
	c := &Checkpoint{
		path: path,
		seen: make(map[string]bool),
	}

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// First run — empty checkpoint is fine.
		return c, nil
	}
	if err != nil {
		return nil, err
	}

	var urls []string
	if err := json.Unmarshal(data, &urls); err != nil {
		return nil, err
	}
	for _, u := range urls {
		c.seen[u] = true
	}
	return c, nil
}

// Seen returns true if the URL has been scraped before.
func (c *Checkpoint) Seen(url string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.seen[url]
}

// Mark records a URL as scraped and persists the checkpoint to disk.
// We save after every URL so a crash loses at most the current in-flight batch.
func (c *Checkpoint) Mark(url string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.seen[url] = true
	return c.save()
}

// save serializes the seen map and writes it atomically.
// Same atomic-write pattern as Chapter 5's store.
func (c *Checkpoint) save() error {
	urls := make([]string, 0, len(c.seen))
	for u := range c.seen {
		urls = append(urls, u)
	}

	data, err := json.Marshal(urls)
	if err != nil {
		return err
	}

	// Write to temp, rename — atomic on POSIX.
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}

// Count returns the number of URLs in the checkpoint.
func (c *Checkpoint) Count() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.seen)
}
```

---

## Rate limiter — `internal/ratelimit/limiter.go`

A rate limiter is what separates a polite scraper from a DoS attack. Our token bucket is a buffered channel: the bucket holds N tokens (channel capacity), and a background goroutine refills it on a fixed schedule.

```go
// internal/ratelimit/limiter.go
package ratelimit

import (
	"context"
	"time"
)

// Limiter implements a token-bucket rate limiter using a buffered channel.
//
// The channel represents the bucket:
//   - Capacity = max burst size.
//   - A refill goroutine adds tokens on a fixed interval.
//   - Workers call Wait() to consume a token.
//
// This is simpler than using golang.org/x/time/rate for our use case and
// teaches the channel-as-semaphore pattern clearly.
type Limiter struct {
	tokens chan struct{}
	rate   time.Duration // minimum duration between requests
}

// New creates a Limiter that allows at most `rps` requests per second,
// with a burst capacity of `burst`.
//
// Example: New(2, 5) = 2 requests/second, burst up to 5.
func New(rps float64, burst int) *Limiter {
	l := &Limiter{
		tokens: make(chan struct{}, burst),
		rate:   time.Duration(float64(time.Second) / rps),
	}

	// Pre-fill the bucket to allow immediate bursting.
	for i := 0; i < burst; i++ {
		l.tokens <- struct{}{}
	}

	return l
}

// Start begins the background refill goroutine.
// It stops when ctx is cancelled.
// Call this once after creating the limiter.
func (l *Limiter) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(l.rate)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Non-blocking send: if the bucket is full, drop the token.
				// This is the key property of a token bucket: tokens don't accumulate
				// unboundedly — the channel's capacity caps the burst.
				select {
				case l.tokens <- struct{}{}:
				default:
					// bucket full, discard
				}
			}
		}
	}()
}

// Wait blocks until a token is available or the context is cancelled.
// Returns ctx.Err() if the context was cancelled before a token was obtained.
//
// This is the "acquire" operation in semaphore terminology.
func (l *Limiter) Wait(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.tokens:
		return nil
	}
}
```

**Why a buffered channel and not `time.Sleep`?**

`time.Sleep` in a worker goroutine enforces a per-worker rate, but with N workers you'd still send N requests per sleep interval. The token bucket is shared across all workers — if you want 2 req/s globally, the bucket refills at 2/s regardless of how many workers you have.

---

## HTML parser — `internal/scraper/parser.go`

goquery brings jQuery-style CSS selectors to Go. The HN front page is a simple `<table>` with predictable class names — this parser is stable because it targets semantic class names, not brittle positional selectors like `table:nth-child(3) tr:nth-child(2)`.

```go
// internal/scraper/parser.go
package scraper

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
)

// ParseHNPage parses the HTML body of a Hacker News front page
// and returns the stories it finds.
//
// goquery.NewDocumentFromReader builds a DOM tree from an io.Reader.
// You then traverse it with CSS selectors just like jQuery:
//   doc.Find(".athing") returns all elements with class="athing"
func ParseHNPage(body string, pageURL string) ([]Story, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("parsing HTML: %w", err)
	}

	var stories []Story
	// Each story on HN spans two <tr> elements:
	// Row 1 (.athing): rank, title, link
	// Row 2 (.subtext): score, author, comment count
	//
	// We collect the first row into a map keyed by HN story ID,
	// then merge the second row into the same struct.
	storyMap := make(map[string]*Story)
	var order []string // preserve display order

	doc.Find("tr.athing").Each(func(i int, sel *goquery.Selection) {
		id, exists := sel.Attr("id")
		if !exists {
			return
		}

		story := &Story{
			ScrapedAt: time.Now(),
		}

		// Rank: first <span class="rank"> inside this row.
		rankStr := strings.TrimRight(
			sel.Find("span.rank").Text(),
			".",
		)
		story.Rank, _ = strconv.Atoi(strings.TrimSpace(rankStr))

		// Title and URL: the <a> inside <span.titleline>.
		titleSel := sel.Find("span.titleline > a").First()
		story.Title = strings.TrimSpace(titleSel.Text())

		href, _ := titleSel.Attr("href")
		if strings.HasPrefix(href, "item?id=") {
			// Self-post (Ask HN, Show HN) — the "URL" is the comments page.
			story.URL = "https://news.ycombinator.com/" + href
		} else {
			story.URL = href
		}

		// Comment URL is always on HN.
		story.CommentURL = fmt.Sprintf("https://news.ycombinator.com/item?id=%s", id)

		storyMap[id] = story
		order = append(order, id)
	})

	// Subtext row: score, author, comment count.
	// The subtext row immediately follows its .athing sibling.
	doc.Find("tr.athing").Each(func(i int, sel *goquery.Selection) {
		id, _ := sel.Attr("id")
		story, ok := storyMap[id]
		if !ok {
			return
		}

		// Next sibling row contains the subtext.
		subtext := sel.Next().Find("td.subtext, span.subtext")

		// Score: <span class="score">123 points</span>
		scoreText := subtext.Find("span.score").Text()
		scoreText = strings.TrimSuffix(strings.TrimSpace(scoreText), " points")
		scoreText = strings.TrimSuffix(scoreText, " point")
		story.Points, _ = strconv.Atoi(scoreText)

		// Author: <a class="hnuser">username</a>
		story.Author = strings.TrimSpace(subtext.Find("a.hnuser").Text())

		// Comment count: last <a> in subtext usually links to comments.
		// Text is like "42 comments" (non-breaking space).
		subtext.Find("a").Each(func(_ int, a *goquery.Selection) {
			txt := strings.TrimSpace(a.Text())
			if strings.Contains(txt, "comment") {
				parts := strings.Fields(txt)
				if len(parts) > 0 {
					story.NumComments, _ = strconv.Atoi(parts[0])
				}
			}
		})
	})

	// Assemble in original order.
	for _, id := range order {
		if s, ok := storyMap[id]; ok && s.Title != "" {
			stories = append(stories, *s)
		}
	}

	return stories, nil
}
```

**goquery cheat sheet:**

| jQuery | goquery |
|--------|---------|
| `$(".class")` | `doc.Find(".class")` |
| `$el.text()` | `sel.Text()` |
| `$el.attr("href")` | `sel.Attr("href")` |
| `$el.next()` | `sel.Next()` |
| `$el.each(fn)` | `sel.Each(func(i int, s *goquery.Selection) {...})` |
| `$el.find("a").first()` | `sel.Find("a").First()` |

---

## Worker — `internal/scraper/worker.go`

The worker is the most important goroutine in the pipeline. It must:
- Respect context cancellation at every blocking point.
- Retry transient errors (5xx, network timeouts) with exponential backoff.
- Never crash the whole pipeline on a single bad URL.

```go
// internal/scraper/worker.go
package scraper

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

const (
	maxRetries     = 3
	requestTimeout = 15 * time.Second
)

// WorkerConfig bundles the dependencies a worker needs.
// Passing a config struct (rather than a long parameter list) makes
// it easy to add fields later without breaking callers.
type WorkerConfig struct {
	Client     *http.Client
	Limiter    interface{ Wait(context.Context) error }
	Checkpoint interface {
		Seen(string) bool
		Mark(string) error
	}
}

// worker reads URLs from urlCh, fetches and parses each one, and sends
// results to resultCh.  Errors go to errCh.  The worker exits when urlCh
// is closed or ctx is cancelled.
//
// This function is designed to be launched as a goroutine:
//   go worker(ctx, cfg, urlCh, resultCh, errCh)
func worker(
	ctx context.Context,
	cfg WorkerConfig,
	urlCh <-chan string,
	resultCh chan<- ScrapeResult,
	errCh chan<- ScrapeError,
) {
	for {
		// Two-case select: either read a URL or stop.
		select {
		case <-ctx.Done():
			return
		case url, ok := <-urlCh:
			if !ok {
				// Channel was closed — no more URLs to process.
				return
			}

			// Check checkpoint before doing any network work.
			if cfg.Checkpoint.Seen(url) {
				continue
			}

			// Acquire rate limit token — blocks until polite to proceed.
			if err := cfg.Limiter.Wait(ctx); err != nil {
				// Context cancelled while waiting for token.
				return
			}

			stories, err := fetchAndParse(ctx, cfg.Client, url)
			if err != nil {
				// Send error to error channel; don't stop processing.
				// Non-blocking send: if the error channel is full, drop the error.
				// A full error channel means the aggregator is overwhelmed — it's
				// better to lose an error log than to deadlock the worker.
				select {
				case errCh <- ScrapeError{URL: url, Err: err}:
				default:
				}
				continue
			}

			// Mark URL as successfully scraped before sending results.
			// This way, if we crash during export we won't re-scrape the URL.
			if err := cfg.Checkpoint.Mark(url); err != nil {
				fmt.Printf("warning: checkpoint write failed for %s: %v\n", url, err)
			}

			for _, story := range stories {
				result := ScrapeResult{Story: story, PageURL: url}
				// Sending to resultCh must also be cancellable.
				select {
				case <-ctx.Done():
					return
				case resultCh <- result:
				}
			}
		}
	}
}

// fetchAndParse fetches a URL and parses it as HN HTML.
// It retries up to maxRetries times on transient errors.
func fetchAndParse(ctx context.Context, client *http.Client, url string) ([]Story, error) {
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			// Exponential backoff: 1s, 2s, 4s.
			// We check ctx.Done() so we don't sleep through a shutdown.
			backoff := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
		}

		// Build request with context so the HTTP call is cancellable.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("building request: %w", err)
		}

		// Set a browser-like User-Agent.  Some sites block Go's default
		// "Go-http-client/2.0" user agent outright.
		req.Header.Set("User-Agent", "goscrape/1.0 (educational scraper; +https://github.com/yourname/goscrape)")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		req.Header.Set("Accept-Language", "en-US,en;q=0.9")

		resp, err := client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("fetching %s: %w", url, err)
			continue
		}

		// Always close the body, even if we don't read it.
		body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024)) // 5 MB limit
		resp.Body.Close()

		if err != nil {
			lastErr = fmt.Errorf("reading body of %s: %w", url, err)
			continue
		}

		// Retry on server errors (5xx), not client errors (4xx).
		if resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("server error %d for %s", resp.StatusCode, url)
			continue
		}
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
		}

		stories, err := ParseHNPage(string(body), url)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", url, err)
		}

		return stories, nil
	}

	return nil, fmt.Errorf("all %d attempts failed for %s: %w", maxRetries, url, lastErr)
}
```

**Why `io.LimitReader`?**

Without it, a malicious or misconfigured server could respond with an infinite stream and exhaust your memory. `io.LimitReader(resp.Body, 5*1024*1024)` limits the read to 5 MB and then returns `io.EOF`. Always limit reads from untrusted sources.

---

## Scraper orchestrator — `internal/scraper/scraper.go`

The orchestrator is the glue: it creates channels, launches workers, and manages the pipeline lifecycle with a `sync.WaitGroup`.

```go
// internal/scraper/scraper.go
package scraper

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/schollz/progressbar/v3"
	"github.com/yourname/goscrape/internal/checkpoint"
	"github.com/yourname/goscrape/internal/export"
	"github.com/yourname/goscrape/internal/ratelimit"
)

// Config holds all user-tunable scraper settings.
type Config struct {
	Workers        int
	RequestsPerSec float64
	BurstSize      int
	OutputJSON     string // output file path for JSON, "" to skip
	OutputCSV      string // output file path for CSV, "" to skip
	CheckpointPath string
	URLs           []string // seed URLs to scrape
	MaxPages       int      // maximum pages to scrape (0 = unlimited)
}

// Run is the entry point for the scraper.  It blocks until all URLs are
// processed or the context is cancelled.
func Run(ctx context.Context, cfg Config) error {
	// ── 1. Initialize dependencies ──────────────────────────────────────────

	cp, err := checkpoint.New(cfg.CheckpointPath)
	if err != nil {
		return fmt.Errorf("loading checkpoint: %w", err)
	}

	limiter := ratelimit.New(cfg.RequestsPerSec, cfg.BurstSize)
	limiter.Start(ctx)

	client := newHTTPClient()

	// ── 2. Create channels ────────────────────────────────────────────────
	//
	// Channel sizing matters:
	//   - urlCh: buffered to decouple URL generation from worker startup.
	//     If it were unbuffered, the URL feeder would block until a worker
	//     is ready — fine for small scrapes, but adds latency.
	//   - resultCh: buffered to absorb bursts from workers.  Workers write
	//     results here; the aggregator drains it.  Without buffering,
	//     a slow aggregator (e.g. disk write) would block all workers.
	//   - errCh: small buffer.  Errors are rare and we'd rather drop a log
	//     entry than block a worker.
	urlCh    := make(chan string, cfg.Workers*2)
	resultCh := make(chan ScrapeResult, cfg.Workers*4)
	errCh    := make(chan ScrapeError, 32)

	// ── 3. Launch workers ─────────────────────────────────────────────────
	//
	// WaitGroup tracks when all workers have finished so we know when
	// to close resultCh.  The aggregator exits when resultCh is closed.
	var wg sync.WaitGroup
	workerCfg := WorkerConfig{
		Client:     client,
		Limiter:    limiter,
		Checkpoint: cp,
	}
	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, workerCfg, urlCh, resultCh, errCh)
		}()
	}

	// ── 4. Close resultCh after all workers are done ───────────────────────
	//
	// This is a common Go pattern: a "closer goroutine" waits for the
	// worker WaitGroup and then closes the downstream channel.
	// The aggregator loops over resultCh until it's closed.
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// ── 5. Feed URLs into the queue ────────────────────────────────────────
	//
	// We do this in a separate goroutine so it doesn't block Run().
	// The goroutine exits when it's done feeding or ctx is cancelled.
	go func() {
		defer close(urlCh) // signal workers that no more URLs are coming

		for _, u := range cfg.URLs {
			select {
			case <-ctx.Done():
				return
			case urlCh <- u:
			}
		}

		// If MaxPages is set, generate page URLs (HN uses ?p=2, ?p=3, ...).
		if cfg.MaxPages > 1 {
			for page := 2; page <= cfg.MaxPages; page++ {
				pageURL := fmt.Sprintf("https://news.ycombinator.com/?p=%d", page)
				select {
				case <-ctx.Done():
					return
				case urlCh <- pageURL:
				}
			}
		}
	}()

	// ── 6. Set up exporters ────────────────────────────────────────────────
	var jsonWriter *export.JSONWriter
	var csvWriter  *export.CSVWriter

	if cfg.OutputJSON != "" {
		jsonWriter, err = export.NewJSONWriter(cfg.OutputJSON)
		if err != nil {
			return fmt.Errorf("creating JSON output: %w", err)
		}
		defer jsonWriter.Close()
	}

	if cfg.OutputCSV != "" {
		csvWriter, err = export.NewCSVWriter(cfg.OutputCSV)
		if err != nil {
			return fmt.Errorf("creating CSV output: %w", err)
		}
		defer csvWriter.Close()
	}

	// ── 7. Progress bar ────────────────────────────────────────────────────
	//
	// progressbar uses -1 for "unknown total" — it renders as a spinning bar
	// rather than a percentage.  We use -1 here because we don't know how
	// many stories we'll find until we've scraped all pages.
	bar := progressbar.NewOptions(-1,
		progressbar.OptionSetDescription("Scraping"),
		progressbar.OptionSetWidth(40),
		progressbar.OptionShowCount(),
		progressbar.OptionSetTheme(progressbar.Theme{
			Saucer:        "=",
			SaucerPadding: "-",
			BarStart:      "[",
			BarEnd:        "]",
		}),
	)

	// ── 8. Aggregate results ───────────────────────────────────────────────
	//
	// This is the "fan-in" half of the pipeline.  We drain both resultCh
	// and errCh in the main goroutine.  resultCh is range-iterable because
	// the closer goroutine closes it when all workers are done.
	var totalStories int
	var errors []ScrapeError

	// errCh is read concurrently with resultCh using a separate goroutine
	// so errors don't accumulate and block workers.
	var errWg sync.WaitGroup
	errWg.Add(1)
	go func() {
		defer errWg.Done()
		for se := range errCh {
			errors = append(errors, se)
		}
	}()

	for result := range resultCh {
		totalStories++
		bar.Add(1)

		if jsonWriter != nil {
			if err := jsonWriter.Write(result.Story); err != nil {
				fmt.Printf("\nwarning: JSON write error: %v\n", err)
			}
		}
		if csvWriter != nil {
			if err := csvWriter.Write(result.Story); err != nil {
				fmt.Printf("\nwarning: CSV write error: %v\n", err)
			}
		}
	}

	// Workers are done; drain the error channel.
	close(errCh)
	errWg.Wait()

	bar.Finish()
	fmt.Printf("\n\nScraped %d stories.\n", totalStories)
	if len(errors) > 0 {
		fmt.Printf("%d URLs failed:\n", len(errors))
		for _, e := range errors {
			fmt.Printf("  - %s: %v\n", e.URL, e.Err)
		}
	}

	return nil
}

// newHTTPClient builds an http.Client with production-appropriate settings.
// The default http.Client has no timeouts — a hung server would block a
// worker goroutine forever.
func newHTTPClient() *http.Client {
	transport := &http.Transport{
		// DialContext timeout: how long to wait for a TCP connection.
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,

		// TLS handshake timeout.
		TLSHandshakeTimeout: 10 * time.Second,

		// How long to wait for the server's first response byte after the
		// request headers have been sent.
		ResponseHeaderTimeout: 10 * time.Second,

		// Enable HTTP/2.
		ForceAttemptHTTP2: true,

		// Pool up to 10 idle connections per host.
		MaxIdleConnsPerHost: 10,

		// For educational purposes — in production, always verify certificates.
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}

	return &http.Client{
		Transport: transport,
		Timeout:   requestTimeout,

		// Custom redirect policy: follow up to 5 redirects but log them.
		// The default policy follows up to 10 redirects silently.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return fmt.Errorf("too many redirects (%d)", len(via))
			}
			return nil
		},
	}
}
```

**The WaitGroup + channel closing pattern is the most important concurrency pattern in this chapter.** Let's trace through it:

1. N worker goroutines read from `urlCh`. Each calls `wg.Done()` when it returns.
2. A "closer" goroutine calls `wg.Wait()` then `close(resultCh)`.
3. The main goroutine iterates `for result := range resultCh {...}`.
4. When `resultCh` is closed, the range loop exits — the main goroutine knows all work is done.

This is a clean, deadlock-free pipeline termination. The alternative — having workers close `resultCh` directly — would cause a panic if two workers both tried to close the same channel.

---

## JSON exporter — `internal/export/json.go`

We stream results directly to disk rather than buffering everything in memory. For 30 HN pages this doesn't matter much, but the principle is important: a scraper that buffers all results in a slice will OOM on a large crawl.

```go
// internal/export/json.go
package export

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/yourname/goscrape/internal/scraper"
)

// JSONWriter streams stories to a JSON file.
// The output is a JSON array: [ {...}, {...}, ... ]
// We write the opening bracket, then each story as a separate JSON object
// separated by commas, then the closing bracket on Close().
type JSONWriter struct {
	file    *os.File
	encoder *json.Encoder
	count   int
}

// NewJSONWriter opens the output file and writes the opening bracket.
func NewJSONWriter(path string) (*JSONWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating JSON output %s: %w", path, err)
	}

	if _, err := f.WriteString("[\n"); err != nil {
		f.Close()
		return nil, err
	}

	return &JSONWriter{
		file:    f,
		encoder: json.NewEncoder(f),
	}, nil
}

// Write appends one story to the JSON array.
// json.Encoder writes the JSON object followed by a newline.
// We prepend a comma for every item after the first to produce valid JSON.
func (w *JSONWriter) Write(s scraper.Story) error {
	if w.count > 0 {
		if _, err := w.file.WriteString(","); err != nil {
			return err
		}
	}
	w.encoder.SetIndent("", "  ")
	if err := w.encoder.Encode(s); err != nil {
		return fmt.Errorf("encoding story: %w", err)
	}
	w.count++
	return nil
}

// Close writes the closing bracket and flushes the file.
func (w *JSONWriter) Close() error {
	if _, err := w.file.WriteString("]\n"); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}
```

---

## CSV exporter — `internal/export/csv.go`

```go
// internal/export/csv.go
package export

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"

	"github.com/yourname/goscrape/internal/scraper"
)

// CSVWriter streams stories to a CSV file using encoding/csv.
// encoding/csv handles quoting, escaping, and line endings correctly —
// never manually build CSV with string concatenation.
type CSVWriter struct {
	file   *os.File
	writer *csv.Writer
}

var csvHeaders = []string{
	"rank", "title", "url", "points", "author",
	"comment_url", "num_comments", "scraped_at",
}

// NewCSVWriter creates the output file and writes the header row.
func NewCSVWriter(path string) (*CSVWriter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("creating CSV output %s: %w", path, err)
	}

	w := csv.NewWriter(f)
	if err := w.Write(csvHeaders); err != nil {
		f.Close()
		return nil, fmt.Errorf("writing CSV header: %w", err)
	}

	return &CSVWriter{file: f, writer: w}, nil
}

// Write appends one story as a CSV row.
func (w *CSVWriter) Write(s scraper.Story) error {
	row := []string{
		strconv.Itoa(s.Rank),
		s.Title,
		s.URL,
		strconv.Itoa(s.Points),
		s.Author,
		s.CommentURL,
		strconv.Itoa(s.NumComments),
		s.ScrapedAt.Format("2006-01-02T15:04:05Z"),
	}
	if err := w.writer.Write(row); err != nil {
		return fmt.Errorf("writing CSV row: %w", err)
	}
	// Flush after every row — for streaming output.
	// csv.Writer buffers internally; Flush() drains the buffer.
	w.writer.Flush()
	return w.writer.Error()
}

// Close flushes and closes the file.
func (w *CSVWriter) Close() error {
	w.writer.Flush()
	if err := w.writer.Error(); err != nil {
		w.file.Close()
		return err
	}
	return w.file.Close()
}
```

---

## CLI — `cmd/root.go`

```go
// cmd/root.go
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "goscrape",
	Short: "A concurrent web scraper for Hacker News",
	Long: `goscrape scrapes Hacker News stories concurrently and exports
them to JSON and CSV files. It supports resume-from-checkpoint,
configurable concurrency, and polite rate limiting.`,
	SilenceUsage: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(scrapeCmd)
}
```

---

## CLI — `cmd/scrape.go`

```go
// cmd/scrape.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/yourname/goscrape/internal/scraper"
)

var scrapeCmd = &cobra.Command{
	Use:   "scrape",
	Short: "Scrape Hacker News front pages",
	Example: `  goscrape scrape
  goscrape scrape --workers 5 --pages 3 --output-json stories.json
  goscrape scrape --workers 10 --rps 2 --output-csv stories.csv`,
	Args: cobra.NoArgs,
	RunE: runScrape,
}

func init() {
	scrapeCmd.Flags().IntP("workers", "w", 3, "number of concurrent worker goroutines")
	scrapeCmd.Flags().IntP("pages", "p", 1, "number of HN pages to scrape (max 10)")
	scrapeCmd.Flags().Float64("rps", 1.0, "max requests per second (across all workers)")
	scrapeCmd.Flags().Int("burst", 3, "token bucket burst size")
	scrapeCmd.Flags().String("output-json", "stories.json", "JSON output file path (empty to skip)")
	scrapeCmd.Flags().String("output-csv", "", "CSV output file path (empty to skip)")
	scrapeCmd.Flags().String("checkpoint", "checkpoint.json", "checkpoint file path for resume support")
}

func runScrape(cmd *cobra.Command, args []string) error {
	workers, _    := cmd.Flags().GetInt("workers")
	pages, _      := cmd.Flags().GetInt("pages")
	rps, _        := cmd.Flags().GetFloat64("rps")
	burst, _      := cmd.Flags().GetInt("burst")
	outputJSON, _ := cmd.Flags().GetString("output-json")
	outputCSV, _  := cmd.Flags().GetString("output-csv")
	cpPath, _     := cmd.Flags().GetString("checkpoint")

	if workers < 1 || workers > 50 {
		return fmt.Errorf("--workers must be between 1 and 50")
	}
	if pages < 1 || pages > 10 {
		return fmt.Errorf("--pages must be between 1 and 10")
	}
	if rps <= 0 {
		return fmt.Errorf("--rps must be positive")
	}

	cfg := scraper.Config{
		Workers:        workers,
		RequestsPerSec: rps,
		BurstSize:      burst,
		OutputJSON:     outputJSON,
		OutputCSV:      outputCSV,
		CheckpointPath: cpPath,
		URLs:           []string{"https://news.ycombinator.com/"},
		MaxPages:       pages,
	}

	// Create a cancellable context.
	// context.WithCancel gives us a cancel function we can call on Ctrl+C.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Intercept SIGINT (Ctrl+C) and SIGTERM (Docker stop / kill).
	// When either signal arrives, cancel the context.
	// All goroutines that check ctx.Done() will receive the cancellation
	// signal and exit gracefully.  In-flight HTTP requests that were built
	// with http.NewRequestWithContext will also be cancelled.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigCh:
			fmt.Println("\nShutting down gracefully...")
			cancel()
		case <-ctx.Done():
		}
	}()

	fmt.Printf("Starting scrape: %d workers, %.1f req/s, %d pages\n", workers, rps, pages)
	return scraper.Run(ctx, cfg)
}
```

---

## Entry point — `main.go`

```go
// main.go
package main

import "github.com/yourname/goscrape/cmd"

func main() {
	cmd.Execute()
}
```

---

## Running the scraper

```bash
# Build
go build -o bin/goscrape .

# Scrape 1 page (default), 3 workers, output to stories.json
./bin/goscrape scrape

# Scrape 3 pages, 5 workers, 2 req/s, export both JSON and CSV
./bin/goscrape scrape --pages 3 --workers 5 --rps 2 \
    --output-json stories.json --output-csv stories.csv

# Resume a previous run (skips URLs already in checkpoint.json)
./bin/goscrape scrape --pages 3 --checkpoint checkpoint.json

# Press Ctrl+C anytime — in-flight requests finish, then the program exits cleanly.
```

---

## Concurrency deep dive: why channels, not mutexes?

You could build this scraper entirely with mutexes and a shared slice. Here's why the channel-based pipeline is better for this use case:

**Separation of concerns.** Each goroutine has a single responsibility and communicates through typed channels. Adding a new stage (e.g. a deduplication filter between fetching and parsing) means inserting a new channel and goroutine — no existing code changes.

**Backpressure.** Buffered channels provide natural flow control. If the exporter is slow (writing a large CSV), `resultCh` fills up, which causes workers to block on their `resultCh <- result` send. The workers then block less frequently on `urlCh <- url` reads, which slows the URL feeder. The whole pipeline naturally throttles without any explicit pressure valve.

**Graceful shutdown.** Context cancellation propagates through `select` statements at every blocking point. With a mutex-guarded loop, you'd need a `stopped bool` flag and a mechanism to wake sleeping goroutines.

**Testability.** You can test `worker` by passing mock channels and a mock HTTP client. You can test `ParseHNPage` with static HTML. The pipeline is built from independently testable parts.

---

## Testing the pipeline

```go
// internal/scraper/worker_test.go
package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// mockLimiter is a no-op rate limiter for tests — we don't want real delays.
type mockLimiter struct{}
func (m *mockLimiter) Wait(ctx context.Context) error { return nil }

// mockCheckpoint tracks seen URLs in memory — no disk I/O in tests.
type mockCheckpoint struct{ seen map[string]bool }
func (m *mockCheckpoint) Seen(url string) bool    { return m.seen[url] }
func (m *mockCheckpoint) Mark(url string) error   { m.seen[url] = true; return nil }

func TestWorker_FetchesAndParses(t *testing.T) {
	// Start a test HTTP server that serves static HN HTML.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		// Minimal valid HN-like HTML.
		fmt.Fprint(w, `<html><body>
		<table>
		  <tr class="athing" id="12345">
		    <td><span class="rank">1.</span></td>
		    <td><span class="titleline"><a href="https://example.com">Test Story</a></span></td>
		  </tr>
		  <tr>
		    <td class="subtext">
		      <span class="score">42 points</span> by
		      <a class="hnuser">testuser</a> |
		      <a href="item?id=12345">10 comments</a>
		    </td>
		  </tr>
		</table>
		</body></html>`)
	}))
	defer ts.Close()

	urlCh    := make(chan string, 1)
	resultCh := make(chan ScrapeResult, 10)
	errCh    := make(chan ScrapeError, 10)

	urlCh <- ts.URL
	close(urlCh)

	cfg := WorkerConfig{
		Client:  ts.Client(),
		Limiter: &mockLimiter{},
		Checkpoint: &mockCheckpoint{seen: make(map[string]bool)},
	}

	ctx := context.Background()
	worker(ctx, cfg, urlCh, resultCh, errCh)
	close(resultCh)
	close(errCh)

	var results []ScrapeResult
	for r := range resultCh {
		results = append(results, r)
	}

	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}
	if results[0].Story.Title != "Test Story" {
		t.Errorf("expected title 'Test Story', got %q", results[0].Story.Title)
	}
}
```

---

## Context propagation — the most important concept

Every goroutine in this program accepts a `context.Context` as its first parameter. This is the Go idiom for cooperative cancellation.

When the user presses Ctrl+C:
1. `signal.Notify` receives the signal on `sigCh`.
2. `cancel()` is called, closing `ctx.Done()`.
3. The rate limiter's `Wait()` returns `ctx.Err()`.
4. Workers see the error and return.
5. Workers decrement the WaitGroup.
6. The closer goroutine calls `close(resultCh)`.
7. The main goroutine's `range resultCh` loop exits.
8. Exporters are closed via `defer`.
9. The program exits cleanly.

Each blocking operation in the pipeline has a `select` with `ctx.Done()`:

```go
// In the URL feeder:
select {
case <-ctx.Done():    // <- shutdown signal received
    return
case urlCh <- url:   // <- normal case
}

// In the rate limiter:
select {
case <-ctx.Done():   // <- shutdown
    return ctx.Err()
case <-l.tokens:     // <- normal
    return nil
}

// In the worker, sending results:
select {
case <-ctx.Done():   // <- shutdown
    return
case resultCh <- result:  // <- normal
}
```

Without this pattern, Ctrl+C would terminate the process without closing files, leaving partial JSON with a missing `]`.

---

## Key patterns learned in this chapter

| Pattern | Where used | Why |
|---------|-----------|-----|
| Fan-out / fan-in | `scraper.go` | N workers feed one aggregator |
| WaitGroup + channel close | `scraper.go` | Clean pipeline termination without races |
| Buffered channel as token bucket | `limiter.go` | Global rate limiting across N goroutines |
| `io.LimitReader` | `worker.go` | Prevent memory exhaustion from runaway responses |
| `http.NewRequestWithContext` | `worker.go` | HTTP calls respect context cancellation |
| Streaming export | `json.go`, `csv.go` | Constant memory usage regardless of result count |
| `signal.Notify` + context cancel | `cmd/scrape.go` | Graceful shutdown on Ctrl+C |
| Test HTTP server | `worker_test.go` | Test HTTP code without real network |

---

## Exercises

1. Add a `--depth 2` flag that follows links from each story page and scrapes the comments. Each comments page URL goes back into `urlCh`.
2. Replace the JSON array format with NDJSON (newline-delimited JSON) — one JSON object per line. This allows streaming parsing of the output file without loading it entirely into memory.
3. Add a `--since 24h` flag that skips any story with `ScrapedAt` older than the given duration (read from checkpoint metadata).
4. Implement a proper `golang.org/x/time/rate` rate limiter and compare its behavior to the token-bucket channel implementation under bursting conditions.
5. Add a `goscrape serve` command that exposes the scraped stories as a JSON API on `localhost:8080`. Combine this with Chapter 4's REST API skills.
