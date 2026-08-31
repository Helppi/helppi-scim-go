package scim

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultPageSize is a compromise: large enough that a full walk of a few tens
// of thousands of records is a handful of requests, small enough that a retry
// is cheap.
const DefaultPageSize = 200

// Options configures a Client. Only BaseURL and Token are required.
type Options struct {
	// BaseURL is the directory root, e.g. https://.../scim/v2. Paths such as
	// Users are resolved under it.
	BaseURL string
	// Token is the bearer credential issued for this partner.
	Token string

	// HTTPClient defaults to a client with a 30s timeout.
	HTTPClient *http.Client
	// MaxRetries applies to network errors, 429 and 5xx. Default 4.
	MaxRetries int
	// RequestsPerSecond throttles outbound calls. Zero disables throttling.
	RequestsPerSecond float64
	// UserAgent identifies your worker in the directory's audit log. Set it to
	// something that names your company and service; the default names only
	// this library.
	UserAgent string

	// Sleep is injectable so tests never actually wait.
	Sleep func(context.Context, time.Duration) error
	// Backoff returns the wait before attempt n (0-based).
	Backoff func(attempt int) time.Duration
}

// Client is a read-mostly SCIM 2.0 client. It is safe for concurrent use.
type Client struct {
	base       *url.URL
	token      string
	hc         *http.Client
	maxRetries int
	userAgent  string
	limiter    *limiter
	sleep      func(context.Context, time.Duration) error
	backoff    func(int) time.Duration
}

// New builds a Client.
func New(opts Options) (*Client, error) {
	if opts.BaseURL == "" {
		return nil, errors.New("scim: BaseURL is required")
	}
	if opts.Token == "" {
		return nil, errors.New("scim: Token is required")
	}
	base, err := url.Parse(strings.TrimRight(opts.BaseURL, "/") + "/")
	if err != nil {
		return nil, fmt.Errorf("scim: bad BaseURL: %w", err)
	}
	c := &Client{
		base:       base,
		token:      opts.Token,
		hc:         opts.HTTPClient,
		maxRetries: opts.MaxRetries,
		userAgent:  opts.UserAgent,
		limiter:    newLimiter(opts.RequestsPerSecond),
		sleep:      opts.Sleep,
		backoff:    opts.Backoff,
	}
	if c.hc == nil {
		c.hc = &http.Client{Timeout: 30 * time.Second}
	}
	if c.maxRetries == 0 {
		c.maxRetries = 4
	}
	if c.userAgent == "" {
		c.userAgent = "helppi-scim-go/1.0"
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	if c.backoff == nil {
		c.backoff = defaultBackoff
	}
	return c, nil
}

// ListUsers walks every page matching filter and calls fn once per record, in
// the order the directory returned them.
//
// An error from fn aborts the walk and is returned unchanged, so the caller can
// tell "the directory failed" from "applying a record failed" — which is what
// decides whether the checkpoint may advance.
func (c *Client) ListUsers(ctx context.Context, filter string, pageSize int, fn func(User) error) error {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}

	var (
		startIndex   = 1
		pages        int
		maxPages     int // 0 until the first response tells us how many to expect
		prevFirstID  string
		totalScanned int
	)

	for {
		q := url.Values{}
		q.Set("startIndex", strconv.Itoa(startIndex))
		q.Set("count", strconv.Itoa(pageSize))
		if filter != "" {
			q.Set("filter", filter)
		}

		var lr ListResponse
		if err := c.do(ctx, http.MethodGet, "Users?"+q.Encode(), nil, &lr); err != nil {
			return fmt.Errorf("list users at startIndex=%d: %w", startIndex, err)
		}
		// A response that is not a SCIM ListResponse must not be read as an
		// empty directory: that turns a proxy error page into "nobody works
		// here any more".
		if !slices.Contains(lr.Schemas, SchemaList) {
			return fmt.Errorf("scim: response at startIndex=%d is not a %s (schemas=%v)",
				startIndex, SchemaList, lr.Schemas)
		}
		if len(lr.Resources) == 0 {
			return nil
		}

		pages++
		if maxPages == 0 && lr.TotalResults > 0 {
			// Generous: the directory may grow while we walk it.
			maxPages = lr.TotalResults/pageSize + 8
		}

		var firstID string
		for i, raw := range lr.Resources {
			var u User
			if err := json.Unmarshal(raw, &u); err != nil {
				return fmt.Errorf("decode record %d of page starting at %d: %w", i, startIndex, err)
			}
			if i == 0 {
				firstID = u.ID
			}
			if err := fn(u); err != nil {
				return err
			}
		}
		totalScanned += len(lr.Resources)

		// A directory that ignores startIndex (or a proxy caching page one)
		// would otherwise keep us requesting the same page forever.
		if firstID != "" && firstID == prevFirstID {
			return fmt.Errorf("scim: directory returned the same first record (%s) twice at startIndex=%d; "+
				"it appears to be ignoring startIndex", firstID, startIndex)
		}
		prevFirstID = firstID

		if maxPages > 0 && pages >= maxPages {
			return fmt.Errorf("scim: pagination did not terminate after %d pages (%d records read, "+
				"totalResults=%d)", pages, totalScanned, lr.TotalResults)
		}

		startIndex += len(lr.Resources)
		// totalResults can shift mid-walk (someone is hired while we page), so
		// the short page is the real terminator and totalResults is a hint.
		if len(lr.Resources) < pageSize || startIndex > lr.TotalResults {
			return nil
		}
	}
}

// GetUser reads one record by directory id.
func (c *Client) GetUser(ctx context.Context, id string) (*User, error) {
	var u User
	if err := c.do(ctx, http.MethodGet, "Users/"+url.PathEscape(id), nil, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// PatchExternalID writes your own identifier for this person back onto the
// directory record. It is
// the only write this client performs, and it is idempotent: sending the same
// value twice is accepted and changes nothing.
func (c *Client) PatchExternalID(ctx context.Context, id, externalID string) (*User, error) {
	if externalID == "" {
		return nil, errors.New("scim: refusing to write an empty externalId")
	}
	body, err := json.Marshal(NewExternalIDPatch(externalID))
	if err != nil {
		return nil, err
	}
	var u User
	if err := c.do(ctx, http.MethodPatch, "Users/"+url.PathEscape(id), body, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

// ServiceProviderConfig reports the directory's advertised capabilities. Read
// it at startup instead of assuming: a directory that does not support filter
// makes incremental synchronization impossible, and one that does not support
// PATCH makes the write-back impossible.
func (c *Client) ServiceProviderConfig(ctx context.Context) (*ProviderConfig, error) {
	var cfg ProviderConfig
	if err := c.do(ctx, http.MethodGet, "ServiceProviderConfig", nil, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Client) do(ctx context.Context, method, ref string, body []byte, out any) error {
	u, err := c.base.Parse(ref)
	if err != nil {
		return fmt.Errorf("scim: bad path %q: %w", ref, err)
	}
	target := u.String()

	for attempt := 0; ; attempt++ {
		if err := c.limiter.wait(ctx, c.sleep); err != nil {
			return err
		}

		wait, retry, err := c.attempt(ctx, method, target, body, out)
		if !retry {
			return err
		}
		if ctx.Err() != nil || attempt >= c.maxRetries {
			return err
		}
		if wait <= 0 {
			wait = c.backoff(attempt)
		}
		if werr := c.sleep(ctx, wait); werr != nil {
			return werr
		}
	}
}

// attempt performs exactly one request. Keeping it in its own function is what
// lets the response body be closed with defer: a defer inside the retry loop
// would hold every attempt's connection open until the whole call returned.
//
// It returns retry=true when the caller should back off and try again; err is
// then the failure that would be reported if retries run out.
func (c *Client) attempt(ctx context.Context, method, target string, body []byte, out any) (wait time.Duration, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, method, target, nil)
	if err != nil {
		return 0, false, err
	}
	if body != nil {
		// Rebuild the reader on every attempt: retrying a request whose body
		// was already consumed is the classic Go retry bug.
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
		req.Header.Set("Content-Type", ContentType)
	}
	req.Header.Set("Accept", ContentType)
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, true, fmt.Errorf("scim: %s %s: %w", method, req.URL.Path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		after, _ := retryAfter(resp)
		scimErr := decodeError(resp)
		return after, true, scimErr
	}
	if resp.StatusCode >= 400 {
		return 0, false, decodeError(resp)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return 0, false, nil
	}

	// A captive portal, a WAF block page or a misrouted proxy answers 200 with
	// HTML. Decoding that yields a zero-value struct, which downstream reads as
	// an empty directory — so refuse it here instead.
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(strings.ToLower(ct), "json") {
		return 0, false, fmt.Errorf("scim: %s %s: expected a JSON response, got Content-Type %q",
			method, req.URL.Path, ct)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return 0, false, fmt.Errorf("scim: decode %s %s: %w", method, req.URL.Path, err)
	}
	return 0, false, nil
}

// retryAfter understands both forms of the header: a delay in seconds, and an
// HTTP date.
func retryAfter(resp *http.Response) (time.Duration, bool) {
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}

// defaultBackoff is equal jitter: half the exponential delay, plus a random
// half. It spreads retries when many workers fail at the same moment without
// letting any single wait collapse to zero.
func defaultBackoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt)) * 500 * time.Millisecond
	if base > 30*time.Second {
		base = 30 * time.Second
	}
	return base/2 + time.Duration(rand.Int64N(int64(base)/2+1))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
