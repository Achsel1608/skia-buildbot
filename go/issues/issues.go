package issues

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"go.skia.org/infra/go/util"
)

const (
	CODESITE_BASE_URL = "https://www.googleapis.com/projecthosting/v2/projects/skia/issues"
	// Switch to this once monorail goes to prod for Skia.
	// MONORAIL_BASE_URL= "https://monorail-prod.appspot.com/_ah/api/monorail/v1/projects/skia/issues"
	MONORAIL_BASE_URL = "https://monorail-staging.appspot.com/_ah/api/monorail/v1/projects/skia/issues"
)

// IssueTracker is a genric interface to an issue tracker that allows us
// to connect issues with items (identified by an id).
type IssueTracker interface {
	// FromQueury returns issue that match the given query string.
	FromQuery(q string) ([]Issue, error)
}

// CodesiteIssueTracker implements IssueTracker.
type CodesiteIssueTracker struct {
	apiKey string
	client *http.Client
}

func NewIssueTracker(apiKey string) IssueTracker {
	return &CodesiteIssueTracker{
		apiKey: apiKey,
		client: util.NewTimeoutClient(),
	}
}

// Issue is an individual issue returned from the project hosting response.
type Issue struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

// IssueResponse is used to decode JSON responses from the project hosting API.
type IssueResponse struct {
	Items []Issue `json:"items"`
}

// FromQuery is part of the IssueTracker interface. See documentation there.
func (c *CodesiteIssueTracker) FromQuery(q string) ([]Issue, error) {
	query := url.Values{}
	query.Add("q", q)
	query.Add("key", c.apiKey)
	query.Add("fields", "items/id,items/state,items/title")
	return get(c.client, CODESITE_BASE_URL+"?"+query.Encode())
}

// MonorailIssueTracker implements IssueTracker.
type MonorailIssueTracker struct {
	client *http.Client
}

func NewMonorailIssueTracker(client *http.Client) IssueTracker {
	return &MonorailIssueTracker{
		client: client,
	}
}

// FromQuery is part of the IssueTracker interface. See documentation there.
func (m *MonorailIssueTracker) FromQuery(q string) ([]Issue, error) {
	query := url.Values{}
	query.Add("q", q)
	query.Add("fields", "items/id,items/state,items/title")
	return get(m.client, MONORAIL_BASE_URL+"?"+query.Encode())
}

func get(client *http.Client, url string) ([]Issue, error) {
	resp, err := client.Get(url)
	if err != nil || resp == nil || resp.StatusCode != 200 {
		return nil, fmt.Errorf("Failed to retrieve issue tracker response: %s", err)
	}
	defer util.Close(resp.Body)

	issueResponse := &IssueResponse{
		Items: []Issue{},
	}
	if err := json.NewDecoder(resp.Body).Decode(&issueResponse); err != nil {
		return nil, err
	}

	return issueResponse.Items, nil
}
