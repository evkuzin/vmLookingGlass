// Package netbox discovers vmagent hostnames from the NetBox inventory.
package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

const (
	// pageLimit is the largest page NetBox serves; the rest is followed through next.
	pageLimit = 200
	// maxPages bounds one tag's pages, so a cyclic or runaway next cannot spin
	// for the whole refresh timeout.
	maxPages = 1000
	// maxPageBytes caps one decoded page, so a hostile response cannot grow the
	// heap without bound.
	maxPageBytes = 16 << 20
)

// Client queries the NetBox inventory.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient returns a Client for the NetBox API rooted at baseURL.
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{},
	}
}

// devicePage is one page of the device list endpoint.
type devicePage struct {
	Next    *string `json:"next"`
	Results []struct {
		Name string `json:"name"`
	} `json:"results"`
}

// Devices returns the sorted, de-duplicated names of the devices carrying any
// of tags. NetBox ANDs repeated tag parameters, so each tag is a separate query.
func (c *Client) Devices(ctx context.Context, tags []string) ([]string, error) {
	seen := make(map[string]struct{})
	for _, tag := range tags {
		if err := c.tagDevices(ctx, tag, seen); err != nil {
			return nil, fmt.Errorf("tag %s: %w", tag, err)
		}
	}

	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// tagDevices adds every page of one tag's devices to seen.
func (c *Client) tagDevices(ctx context.Context, tag string, seen map[string]struct{}) error {
	q := url.Values{}
	q.Set("tag", tag)
	q.Set("limit", strconv.Itoa(pageLimit))
	// brief=1 drops everything but the names, which is all discovery needs.
	q.Set("brief", "1")

	for next, pages := c.baseURL+"/dcim/devices/?"+q.Encode(), 0; next != ""; pages++ {
		if pages == maxPages {
			return fmt.Errorf("more than %d pages", maxPages)
		}
		var page devicePage
		if err := c.get(ctx, next, &page); err != nil {
			return err
		}
		if len(page.Results) > pageLimit {
			return fmt.Errorf("page has %d results, limit is %d", len(page.Results), pageLimit)
		}
		for _, d := range page.Results {
			seen[d.Name] = struct{}{}
		}
		if page.Next == nil {
			return nil
		}
		next = *page.Next
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return fmt.Errorf("read devices: %w", err)
	}
	if len(body) > maxPageBytes {
		return fmt.Errorf("page exceeds %d bytes", maxPageBytes)
	}
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("decode devices: %w", err)
	}
	return nil
}
