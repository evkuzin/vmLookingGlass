package netbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writePage serves one page of the device list.
func writePage(w http.ResponseWriter, names []string, next string) {
	results := make([]map[string]any, 0, len(names))
	for i, n := range names {
		results = append(results, map[string]any{"id": i + 1, "name": n})
	}
	page := map[string]any{"count": len(names), "results": results}
	if next != "" {
		page["next"] = next
	}
	_ = json.NewEncoder(w).Encode(page)
}

func TestDevicesUnionsTagsAndFollowsPages(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q, want application/json", got)
		}
		q := r.URL.Query()
		tag, page := q.Get("tag"), q.Get("page")
		if page == "" {
			if got := q.Get("limit"); got != "200" {
				t.Errorf("limit = %q, want 200", got)
			}
			if got := q.Get("brief"); got != "1" {
				t.Errorf("brief = %q, want 1", got)
			}
		}

		switch {
		case tag == "a" && page == "":
			writePage(w, []string{"a1", "shared"}, srv.URL+"/dcim/devices/?tag=a&page=2")
		case tag == "a" && page == "2":
			writePage(w, []string{"a2"}, "")
		case tag == "b" && page == "":
			writePage(w, []string{"shared", "b1"}, srv.URL+"/dcim/devices/?tag=b&page=2")
		case tag == "b" && page == "2":
			writePage(w, []string{"b2"}, "")
		default:
			t.Errorf("unexpected request %q", r.URL)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	// The trailing slash must not produce a doubled separator.
	names, err := NewClient(srv.URL+"/").Devices(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Devices: %v", err)
	}
	want := []string{"a1", "a2", "b1", "b2", "shared"}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}
}

func TestDevicesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient(srv.URL).Devices(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error for HTTP 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Errorf("error %q does not name the status", err)
	}
}

func TestDevicesContextCanceled(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	done := make(chan error, 1)
	go func() {
		_, err := NewClient(srv.URL).Devices(ctx, []string{"a"})
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected an error after cancellation")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Devices did not return after cancellation")
	}
}

func TestDevicesDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not json")
	}))
	t.Cleanup(srv.Close)

	if _, err := NewClient(srv.URL).Devices(context.Background(), []string{"a"}); err == nil {
		t.Fatal("expected a decode error")
	}
}

func TestDevicesRejectsOversizedPage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePage(w, hostNames(pageLimit+1), "")
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient(srv.URL).Devices(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error for a page over the limit")
	}
	for _, want := range []string{fmt.Sprint(pageLimit + 1), fmt.Sprint(pageLimit)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %s", err, want)
		}
	}
}

func TestDevicesRejectsCyclicNext(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePage(w, []string{"a1"}, srv.URL+"/dcim/devices/?tag=a")
	}))
	t.Cleanup(srv.Close)

	_, err := NewClient(srv.URL).Devices(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected an error for a cyclic next")
	}
	if want := fmt.Sprint(maxPages); !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the page cap %s", err, want)
	}
}
