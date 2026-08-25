// THE DASHBOARD READ over HTTP: the rows of the named metric series.
//
// The rules are in the store - see internal/store/dashboards.go - so this
// file is argument checking and status codes, the shape threadfolds.go has
// and for the same reason: the console renders a declaration and an agent
// pushing rows goes through the artifact door, and both must read the same
// series through the same filter.
//
// WHY THIS DOOR EXISTS AT ALL, and why it is this narrow: the authoring, the
// row read and the list are all the ordinary artifact doors - a dashboard is
// a memory row, its tiles live in fields, and GET /api/artifact/{id} already
// serves them. What does not exist anywhere is "the rows of this metric
// series under the reader's own reach", which is the one query every tile on
// the page is. This door answers it, under the same permission filter every
// list goes through, so a dashboard is no more readable than the rows it
// names.
//
// NOT GET /api/metrics, deliberately: that route is the node measuring
// itself (metrics.go), a different question about a different population,
// and one route answering both would hand a caller one when it asked for the
// other.

package main

import (
	"net/http"
)

// metricsRowsParams are the query parameters this door honours, and the whole
// of them - refuseUnknownParams closes it against everything else, for the
// reason listParams does: a filter that silently did nothing would answer an
// unfiltered page to a caller who asked for a narrowed one.
var metricsRowsParams = map[string]bool{
	"limit": true,
	"name":  true,
}

// GET /api/metrics/rows?name=X&name=Y&limit=N
//
// The rows of the named series, newest first, under the reader's reach. The
// metric name is repeatable because a dashboard page reads every series its
// tiles name in one call, and a caller reading one series at a time is just
// the one-name case of the same shape. A read with no name would be "every
// metric row ever pushed", which is not a question a dashboard asks; it is
// refused rather than answered as a shorter list that reads exactly like "no
// rows".
func (s *server) handleMetricsRows(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if why := refuseUnknownParams(q, metricsRowsParams); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}
	names := q["name"]
	if len(names) == 0 {
		writeJSON(w, http.StatusBadRequest,
			errorBody("name is required - the door answers the rows of the metrics it is asked for"))
		return
	}
	list, err := s.db.Metrics(r.Context(), principalOf(r), names, intParam(q.Get("limit")))
	if err != nil {
		serverError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"metrics": list})
}

// metricsSeriesParams is the door's vocabulary. `points` rather than `limit`
// because it bounds points PER SERIES, and limit already means something else on
// the neighbouring door - one word, one meaning.
var metricsSeriesParams = map[string]bool{"name": true, "points": true}

// handleMetricsSeries answers the last N readings of each named series, oldest
// first.
//
// GET /api/metrics/series?name=A&name=B&points=60
//
// WHY A SECOND DOOR RATHER THAN A FLAG ON THE FIRST. /api/metrics/rows answers
// "what do these metrics say now" and takes one limit across every name, so
// three series at limit 200 can come back as 200 points of the busiest and none
// of the others. That is the right answer to its own question and cannot be the
// right answer to this one. A flag would make one door answer two questions and
// the caller guess which it got.
//
// OLDEST FIRST, said here and in the store, because a sparkline is read left to
// right. Every other list on this node is newest-first; a series drawn backwards
// looks like a trend reversing, which is a wrong answer that renders beautifully
// and is exactly the class this fleet keeps paying for.
func (s *server) handleMetricsSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if why := refuseUnknownParams(q, metricsSeriesParams); why != "" {
		writeJSON(w, http.StatusBadRequest, errorBody(why))
		return
	}
	names := q["name"]
	if len(names) == 0 {
		writeJSON(w, http.StatusBadRequest,
			errorBody("name is required - the door answers the points of the series it is asked for"))
		return
	}
	list, err := s.db.SeriesOf(r.Context(), principalOf(r), names, intParam(q.Get("points")))
	if err != nil {
		serverError(w, r, err)
		return
	}
	// asked is echoed so a caller can tell "this series has no rows" from "I
	// misspelled its name": the series array carries only names that exist, and
	// without the ask there is nothing to diff it against.
	writeJSON(w, http.StatusOK, map[string]any{"series": list, "asked": names})
}
