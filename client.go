package he

import (
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/pkg/errors"
	"golang.org/x/time/rate"
)

const (
	// User agent to use for API requests
	userAgent = "libdns-he/1.2.0"
	// API URL to POST updates to
	updateURL = "https://dyn.dns.he.net/nic/update"

	// How many times to retry after a temporary API error
	maxRetries = 5

	// API rate limit configuration
	rateLimit = 0.125
	rateBurst = 1

	// API error response codes
	codeGood     = "good"
	codeNoChg    = "nochg"
	codeAbuse    = "abuse"
	codeBadAgent = "badagent"
	codeBadAuth  = "badauth"
	codeInterval = "interval"
	codeNoHost   = "nohost"
	codeNotFqdn  = "notfqdn"
	codeNoTXT    = "notxt"
)

var (
	// Set environment variable to "TRUE" to enable debug logging
	debug = (os.Getenv("LIBDNS_HE_DEBUG") == "TRUE")
)

// Query Google DNS for A/AAAA/TXT record for a given DNS name
func (p *Provider) getDomain(ctx context.Context, zone string) ([]libdns.Record, error) {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	var libRecords []libdns.Record

	// The HE API only has an `/update` endpoint and no way
	// to get current records. So instead, we just make
	// simple DNS queries to get the A, AAAA, and TXT records.
	r := net.DefaultResolver

	ips, err := r.LookupHost(ctx, zone)
	if err != nil {
		var dnsErr *net.DNSError
		// Ignore missing dns record
		if !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
			return libRecords, errors.Wrapf(err, "error looking up host")
		}
	}

	for _, ip := range ips {
		parsed, err := netip.ParseAddr(ip)
		if err != nil {
			return libRecords, errors.Wrapf(err, "error parsing ip")
		}

		libRecords = append(libRecords, libdns.Address{
			Name: "@",
			IP:   parsed,
		})
	}

	txt, err := r.LookupTXT(ctx, zone)
	if err != nil {
		var dnsErr *net.DNSError
		// Ignore missing dns record
		if !(errors.As(err, &dnsErr) && dnsErr.IsNotFound) {
			return libRecords, errors.Wrapf(err, "error looking up txt")
		}
	}
	for _, t := range txt {
		if t == "" {
			continue
		}
		libRecords = append(libRecords, libdns.TXT{
			Name: "@",
			Text: t,
		})
	}

	return libRecords, nil
}

// Set or clear the value of a DNS entry
func (p *Provider) setRecord(
	ctx context.Context,
	zone string,
	record libdns.Record,
	clear bool,
) error {
	p.mutex.Lock()
	defer p.mutex.Unlock()

	// Sanitize the domain, combines the zone and record names
	// the record name should typically be relative to the zone
	domain := libdns.AbsoluteName(record.RR().Name, zone)

	params := map[string]string{}

	rr := record.RR()

	if !clear {
		parsedRR, err := rr.Parse()
		if err != nil {
			return errors.Wrapf(err, "error parsing record")
		}

		switch rr := parsedRR.(type) {
		case libdns.TXT:
			params["txt"] = rr.Text
		case libdns.Address:
			params["myip"] = rr.IP.String()
		case libdns.RR:
			return fmt.Errorf("unsupported record type: %s", rr.Type)
		default:
			return fmt.Errorf("unsupported record type: %T", rr)
		}
	} else {
		// When deleting we don't care about the RR data, just the RR type
		switch rr.Type {
		case "TXT":
			params["txt"] = "\"\""
		case "A":
			params["myip"] = "127.0.0.1"
		case "AAAA":
			params["myip"] = "::1"
		default:
			return fmt.Errorf("unsupported record type: %s", rr.Type)
		}
	}

	retries := 0
	for {
		retries += 1

		// Make the API request to HE
		err := p.doRequest(ctx, domain, params)
		if err != nil {
			var urlErr *url.Error
			if errors.As(err, &urlErr) &&
				(urlErr.Temporary() || urlErr.Unwrap().Error() == "EOF") {
				// Temporary error, retry with exponential backoff
				if retries >= maxRetries {
					return err
				}
				time.Sleep(backoff(retries))
				continue
			}
			return err
		}
		break
	}

	return nil
}

// Make HTTP API request to Hurricane Electric
func (p *Provider) doRequest(ctx context.Context, domain string, params map[string]string) error {
	// https://dns.he.net/docs.html

	u, _ := url.Parse(updateURL)

	// Set up the query with the params we always set
	query := u.Query()
	query.Set("hostname", strings.TrimSuffix(domain, "."))
	query.Set("password", p.APIKey)

	// Add the remaining query parameters for this request
	for key, val := range params {
		query.Set(key, val)
	}

	reqBody := strings.NewReader(query.Encode())

	// Create the request
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), reqBody)
	if err != nil {
		return errors.Wrapf(err, "error creating http request")
	}

	// Add HTTP headers
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	if p.rateLimiter == nil {
		// Init rate limiter
		p.rateLimiter = rate.NewLimiter(rateLimit, rateBurst)
	}

	// Wait for tokens from rate limiter
	err = p.rateLimiter.Wait(ctx)
	if err != nil {
		return errors.Wrapf(err, "error waiting for rate limiter")
	}

	// Make HTTP request to HE API update endpoint
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return errors.Wrapf(err, "error making http request")
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return errors.Wrapf(err, "error reading body from http response")
	}

	respBody := string(bodyBytes)
	if err := checkResponse(u, respBody); err != nil {
		if !debug {
			delete(query, "password")
		}

		return errors.Wrapf(err,
			"HE api request failed, query=%s, response=%s", query, respBody,
		)
	}

	return nil
}

type rateLimitExceededError struct{}

func (e *rateLimitExceededError) Error() string   { return "exceeded API rate limit" }
func (e *rateLimitExceededError) Temporary() bool { return true }

// Convert API response code to human friendly error
func checkResponse(uri *url.URL, body string) error {
	code, _, _ := strings.Cut(body, " ")

	switch code {
	case codeGood:
		return nil
	case codeNoChg:
		return nil
	case codeAbuse:
		return fmt.Errorf("blocked for abuse")
	case codeBadAgent:
		return fmt.Errorf("user agent not sent or HTTP method not recognized")
	case codeBadAuth:
		return fmt.Errorf("incorrect authentication key provided")
	case codeInterval:
		// This is a temporary error
		return &url.Error{
			Op:  "Post",
			URL: uri.String(),
			Err: &rateLimitExceededError{},
		}
	case codeNoHost:
		return fmt.Errorf("the record provided does not exist in this account")
	case codeNotFqdn:
		return fmt.Errorf("the record provided isn't an FQDN")
	case codeNoTXT:
		return fmt.Errorf("no dynamic TXT record to update")
	default:
		// This is basically only server errors.
		return fmt.Errorf("unknown server error")
	}
}

// Calculate how many seconds to backoff for a given retry attempt
func backoff(retries int) time.Duration {
	expo := int(math.Pow(2, float64(retries)))

	half := int(expo / 2)

	random := 0
	if half >= 1 {
		random = rand.Intn(half)
	}

	return time.Duration(expo+random) * time.Second
}
