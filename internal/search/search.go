package search

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/mitchellh/mapstructure"
	sailpoint "github.com/sailpoint-oss/golang-sdk/v3"
	sailpointsdk "github.com/sailpoint-oss/golang-sdk/v3/search"
	"github.com/sailpoint-oss/sailpoint-cli/internal/output"
)

// DefaultPageSize is the number of records requested for each page of a search.
const DefaultPageSize int32 = 250

// maxAutoFileNameLength bounds the length of an auto generated result file name so
// that it always fits within the per component limit of common filesystems.
const maxAutoFileNameLength = 100

func ParseIndices(indices string) (sailpointsdk.Index, error) {
	switch indices {
	case "accessprofiles":
		return sailpointsdk.INDEX_ACCESSPROFILES, nil
	case "accountactivities":
		return sailpointsdk.INDEX_ACCOUNTACTIVITIES, nil
	case "entitlements":
		return sailpointsdk.INDEX_ENTITLEMENTS, nil
	case "events":
		return sailpointsdk.INDEX_EVENTS, nil
	case "identities":
		return sailpointsdk.INDEX_IDENTITIES, nil
	case "roles":
		return sailpointsdk.INDEX_ROLES, nil
	default:
		return "*", fmt.Errorf("index provided is invalid")
	}
}

func BuildSearch(searchQuery string, sort []string, indices []string) (sailpointsdk.Search, error) {

	search := sailpointsdk.NewSearch()
	search.Query = sailpointsdk.NewQuery()
	search.QueryResultFilter = sailpointsdk.NewQueryresultfilter()
	search.Query.Query = &searchQuery
	search.Sort = sort
	search.Indices = []sailpointsdk.Index{}

	for i := 0; i < len(indices); i++ {
		tempIndices, err := ParseIndices(indices[i])

		if err != nil {
			return *search, err
		}

		search.Indices = append(search.Indices, tempIndices)
	}

	return *search, nil
}

// ApplyIncludes limits the fields returned for every document by filling in
// queryResultFilter.includes. It is a no-op when no fields are provided, so the
// server keeps returning whole documents.
func ApplyIncludes(search *sailpointsdk.Search, includes []string) {
	if len(includes) == 0 {
		return
	}
	if search.QueryResultFilter == nil {
		search.QueryResultFilter = sailpointsdk.NewQueryresultfilter()
	}
	search.QueryResultFilter.SetIncludes(includes)
}

// ApplySearchAfter shifts the search window to start after the given sort values.
func ApplySearchAfter(search *sailpointsdk.Search, searchAfter []string) {
	if len(searchAfter) == 0 {
		return
	}
	search.SearchAfter = searchAfter
}

// decodeEntries converts the raw documents returned by the search endpoint into the
// typed per index slices of SearchResults.
func decodeEntries(entries []map[string]interface{}, results *SearchResults) error {
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		switch entry["_type"] {
		case "accountactivity":
			var AccountActivity AccountActivity
			err := mapstructure.Decode(entry, &AccountActivity)
			if err != nil {
				return err
			}
			results.AccountActivities = append(results.AccountActivities, AccountActivity)

		case "accessprofile":
			var AccessProfile AccessProfile
			err := mapstructure.Decode(entry, &AccessProfile)
			if err != nil {
				return err
			}
			results.AccessProfiles = append(results.AccessProfiles, AccessProfile)

		case "entitlement":
			var Entitlement Entitlement
			err := mapstructure.Decode(entry, &Entitlement)
			if err != nil {
				return err
			}
			results.Entitlements = append(results.Entitlements, Entitlement)

		case "event":
			var Event Event
			err := mapstructure.Decode(entry, &Event)
			if err != nil {
				return err
			}
			results.Events = append(results.Events, Event)

		case "identity":
			var Identity Identity
			err := mapstructure.Decode(entry, &Identity)
			if err != nil {
				return err
			}
			results.Identities = append(results.Identities, Identity)

		case "role":
			var Role Role
			err := mapstructure.Decode(entry, &Role)
			if err != nil {
				return err
			}
			results.Roles = append(results.Roles, Role)
		}
	}

	return nil
}

func PerformSearch(apiClient sailpoint.APIClient, search sailpointsdk.Search) (SearchResults, error) {
	var SearchResults SearchResults

	ctx := context.TODO()
	resp, r, err := sailpoint.PaginateWithDefaults[map[string]interface{}](apiClient.SearchAPI.SearchPostV1(ctx).Search(search))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	}

	log.Debug("Search complete")

	err = decodeEntries(resp, &SearchResults)
	if err != nil {
		return SearchResults, err
	}

	return SearchResults, nil
}

func PerformSearchWithLimit(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32) (SearchResults, error) {
	var SearchResults SearchResults

	if limit == 0 {
		limit = 500
	}

	ctx := context.TODO()
	resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(limit).Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	}

	log.Debug("Search complete")

	err = decodeEntries(resp, &SearchResults)
	if err != nil {
		return SearchResults, err
	}

	return SearchResults, nil
}

// PerformSearchPage runs a single search request honouring limit and offset, and returns
// the decoded results together with the total match count reported by the server. The
// total is -1 when the server did not send an X-Total-Count header.
func PerformSearchPage(apiClient sailpoint.APIClient, search sailpointsdk.Search, limit int32, offset int32) (SearchResults, int32, error) {
	var SearchResults SearchResults

	if limit == 0 {
		limit = DefaultPageSize
	}

	ctx := context.TODO()
	resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(limit).Offset(offset).Count(true).Execute()
	if err != nil {
		return SearchResults, -1, newSearchError(r, err)
	}

	log.Debug("Search complete", "limit", limit, "offset", offset)

	total := int32(-1)
	if r != nil {
		if n, ok := totalCountFromResponse(r); ok {
			total = n
		}
	}

	if err := decodeEntries(resp, &SearchResults); err != nil {
		return SearchResults, total, err
	}

	return SearchResults, total, nil
}

// PerformSearchAllPages walks a result set larger than the server page limit using
// searchAfter paging, which is not bounded by the 10K offset limit. A stable sort field
// is required, so the id field is used when no sort is given.
func PerformSearchAllPages(apiClient sailpoint.APIClient, search sailpointsdk.Search) (SearchResults, int32, error) {
	var SearchResults SearchResults

	if len(search.Sort) == 0 {
		search.Sort = []string{"id"}
	}

	total := int32(-1)
	for {
		ctx := context.TODO()
		resp, r, err := apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(DefaultPageSize).Count(total < 0).Execute()
		if err != nil {
			return SearchResults, total, newSearchError(r, err)
		}

		if r != nil && total < 0 {
			if n, ok := totalCountFromResponse(r); ok {
				total = n
			}
		}

		if err := decodeEntries(resp, &SearchResults); err != nil {
			return SearchResults, total, err
		}

		log.Debug("Page complete", "records", len(resp), "searchAfter", search.SearchAfter)

		if len(resp) < int(DefaultPageSize) {
			break
		}

		last := resp[len(resp)-1]
		searchAfter := sortValues(last, search.Sort)
		if len(searchAfter) == 0 || strings.Join(searchAfter, "\x00") == "" {
			// Without sort values there is no stable cursor to continue from.
			break
		}
		ApplySearchAfter(&search, searchAfter)
	}

	return SearchResults, total, nil
}

// PerformCount returns the number of documents matching the search. It uses the
// dedicated count endpoint and falls back to a single record search that requests
// the X-Total-Count header.
func PerformCount(apiClient sailpoint.APIClient, search sailpointsdk.Search) (int32, error) {
	ctx := context.TODO()

	r, err := apiClient.SearchAPI.SearchCountV1(ctx).Search(search).Execute()
	if err == nil && r != nil {
		if n, ok := totalCountFromResponse(r); ok {
			return n, nil
		}
	} else if err != nil && r != nil && r.StatusCode != http.StatusNoContent {
		return -1, newSearchError(r, err)
	}

	// Fall back to a minimal search, which populates X-Total-Count when count=true.
	_, r, err = apiClient.SearchAPI.SearchPostV1(ctx).Search(search).Limit(1).Offset(0).Count(true).Execute()
	if err != nil {
		return -1, newSearchError(r, err)
	}
	if r == nil {
		return 0, nil
	}
	if n, ok := totalCountFromResponse(r); ok {
		return n, nil
	}

	return 0, fmt.Errorf("the search count endpoint did not return a total count")
}

// totalCountFromResponse reads the X-Total-Count header of a response.
func totalCountFromResponse(r *http.Response) (int32, bool) {
	if r == nil {
		return 0, false
	}
	raw := strings.TrimSpace(r.Header.Get("X-Total-Count"))
	if raw == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(n), true
}

// newSearchError turns a failed response into an actionable error. A 413 is called out
// explicitly because it is the usual way a whole document search fails on large types.
func newSearchError(r *http.Response, err error) error {
	if r != nil && r.StatusCode == http.StatusRequestEntityTooLarge {
		return fmt.Errorf("413 Content Too Large: the response exceeded the server limit. Retry with --count to only get the number of matches, --includes id,name to return fewer fields, or --limit 50 to return fewer records (original error: %v)", err)
	}
	if err != nil {
		return err
	}
	if r != nil {
		return fmt.Errorf("search failed with status %s", r.Status)
	}
	return fmt.Errorf("search failed")
}

// sortValues returns the values of the sorted fields of a document, which is what the
// searchAfter parameter expects. Nested fields such as attributes.uid are supported.
func sortValues(document map[string]interface{}, sort []string) []string {
	values := make([]string, 0, len(sort))
	for _, entry := range sort {
		field := strings.TrimPrefix(strings.TrimPrefix(entry, "+"), "-")
		value := fieldValue(document, field)
		if value == "" {
			// A missing value makes the cursor ambiguous, so stop paging.
			return values
		}
		values = append(values, value)
	}
	return values
}

// fieldValue walks a dotted field path in a decoded document and renders the value.
func fieldValue(document map[string]interface{}, field string) string {
	var current interface{} = document
	for _, part := range strings.Split(field, ".") {
		asMap, ok := current.(map[string]interface{})
		if !ok {
			return ""
		}
		value, ok := asMap[part]
		if !ok {
			return ""
		}
		current = value
	}

	switch value := current.(type) {
	case nil:
		return ""
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case float64:
		return strconv.FormatFloat(value, 'f', -1, 64)
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprintf("%v", value)
		}
		return string(encoded)
	}
}

// OutputOptions tweaks how results are written by WriteResults. The zero value matches
// the historical behaviour of IterateIndices.
type OutputOptions struct {
	// FileName is the caller chosen file name, without extension. When empty the name
	// is derived from the query and the index.
	FileName string
	// ToStdout writes the JSON to Writer instead of to a file.
	ToStdout bool
	// Writer receives the JSON when ToStdout is set. It defaults to os.Stdout.
	Writer io.Writer
}

// resultGroup is one index worth of results, in a stable order.
type resultGroup struct {
	index string
	value interface{}
}

// resultGroups returns the non-empty per index result sets in a stable order.
func resultGroups(searchResults SearchResults) []resultGroup {
	groups := make([]resultGroup, 0, 6)

	if searchResults.AccountActivities != nil {
		groups = append(groups, resultGroup{index: "AccountActivities", value: searchResults.AccountActivities})
	}
	if searchResults.AccessProfiles != nil {
		groups = append(groups, resultGroup{index: "AccessProfiles", value: searchResults.AccessProfiles})
	}
	if searchResults.Entitlements != nil {
		groups = append(groups, resultGroup{index: "Entitlements", value: searchResults.Entitlements})
	}
	if searchResults.Events != nil {
		groups = append(groups, resultGroup{index: "Events", value: searchResults.Events})
	}
	if searchResults.Identities != nil {
		groups = append(groups, resultGroup{index: "Identities", value: searchResults.Identities})
	}
	if searchResults.Roles != nil {
		groups = append(groups, resultGroup{index: "Roles", value: searchResults.Roles})
	}

	return groups
}

func IterateIndices(SearchResults SearchResults, searchQuery string, folderPath string, outputTypes []string) error {
	if len(SearchResults.AccountActivities) > 0 {
		fileName := "query=" + searchQuery + "&indices=AccountActivities"
		err := SaveResults(SearchResults.AccountActivities, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.AccessProfiles) > 0 {
		fileName := "query=" + searchQuery + "&indices=AccessProfiles"
		err := SaveResults(SearchResults.AccessProfiles, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Entitlements) > 0 {
		fileName := "query=" + searchQuery + "&indices=Entitlements"
		err := SaveResults(SearchResults.Entitlements, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Events) > 0 {
		fileName := "query=" + searchQuery + "&indices=Events"
		err := SaveResults(SearchResults.Events, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Identities) > 0 {
		fileName := "query=" + searchQuery + "&indices=Identities"
		err := SaveResults(SearchResults.Identities, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	if len(SearchResults.Roles) > 0 {
		fileName := "query=" + searchQuery + "&indices=Roles"
		err := SaveResults(SearchResults.Roles, fileName, folderPath, outputTypes)
		if err != nil {
			return err
		}
	}
	return nil
}

// WriteResults writes results using the provided options. Unlike IterateIndices it also
// emits a value for an empty result set, so that scripts can tell "0 results" apart from
// a failed run.
func WriteResults(SearchResults SearchResults, searchQuery string, folderPath string, opts OutputOptions) error {
	groups := resultGroups(SearchResults)

	if opts.ToStdout {
		var writer io.Writer = opts.Writer
		if writer == nil {
			writer = os.Stdout
		}
		if len(groups) == 1 {
			return writeJSON(writer, groups[0].value)
		}
		if len(groups) == 0 {
			// An empty result set is still an array, so that 0 results is not confused
			// with a failed run.
			return writeJSON(writer, []interface{}{})
		}
		return writeJSON(writer, SearchResults)
	}

	for _, group := range groups {
		fileName := opts.FileName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, group.index)
		} else if len(groups) > 1 {
			fileName = fileName + "-" + group.index
		}
		if err := SaveResults(group.value, fileName, folderPath, []string{"json"}); err != nil {
			return err
		}
	}

	// An empty result set still produces a file so that 0 results is not confused with
	// a failed run.
	if len(groups) == 0 {
		fileName := opts.FileName
		if fileName == "" {
			fileName = BuildResultFileName(searchQuery, "Results")
		}
		return SaveResults([]interface{}{}, fileName, folderPath, []string{"json"})
	}

	return nil
}

// BuildResultFileName builds a result file name that is safe for any filesystem by
// truncating long query text and keeping a short hash of the full name.
func BuildResultFileName(searchQuery string, index string) string {
	name := "query=" + searchQuery + "&indices=" + index
	if len(name) <= maxAutoFileNameLength {
		return name
	}

	sum := sha1.Sum([]byte(name))
	tail := hex.EncodeToString(sum[:])
	if len(tail) > 8 {
		tail = tail[:8]
	}

	return name[:maxAutoFileNameLength-len(tail)-1] + "-" + tail
}

// writeJSON renders a value as indented JSON on the given writer.
func writeJSON(writer io.Writer, value interface{}) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	_, err = writer.Write(append(data, '\n'))
	return err
}

func SaveResults[T any](formattedResponse T, fileName string, filePath string, outputTypes []string) error {
	for i := 0; i < len(outputTypes); i++ {
		outputType := outputTypes[i]
		switch outputType {
		case "json":
			savePath := output.GetSanitizedPath(fileName, "json")
			log.Info("Saving Results", "file", savePath)
			err := output.SaveJSONFile(formattedResponse, fileName, filePath)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("invalid output type provided %s", outputType)
		}
	}

	return nil
}
