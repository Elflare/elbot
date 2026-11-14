package management

import (
	"elbot/internal/control"
	"elbot/internal/logging"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (h *handler) queryLogs(w http.ResponseWriter, req *http.Request) {
	q, err := logQuery(req.URL.Query())
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	result, err := h.control.QueryLogs(req.Context(), control.QueryLogsRequest{Category: req.URL.Query().Get("category"), Query: q})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) queryUsage(w http.ResponseWriter, req *http.Request) {
	q, err := logQuery(req.URL.Query())
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	first, err := intQuery(req.URL.Query(), "first", 1)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	last, err := intQuery(req.URL.Query(), "last", 100000)
	if err != nil {
		writeBadQuery(w, err)
		return
	}
	group := req.URL.Query().Get("group_by")
	if group == "" {
		group = "model"
	}
	result, err := h.control.QueryUsage(req.Context(), control.QueryUsageRequest{Query: q, GroupBy: group, First: first, Last: last})
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func (h *handler) checkConfiguration(w http.ResponseWriter, req *http.Request) {
	result, err := h.control.CheckConfiguration(req.Context())
	if writeControlResultError(w, result, err) {
		return
	}
	writeAPIJSON(w, http.StatusOK, result.Data)
}
func writeBadQuery(w http.ResponseWriter, err error) {
	writeAPIError(w, http.StatusBadRequest, "invalid_arguments", err.Error())
}
func intQuery(q url.Values, key string, fallback int) (int, error) {
	value := q.Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}
func int64Query(q url.Values, key string, fallback int64) (int64, error) {
	value := q.Get(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer", key)
	}
	return parsed, nil
}
func boolQuery(q url.Values, key string) (bool, error) {
	value := q.Get(key)
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", key)
	}
	return parsed, nil
}
func logQuery(q url.Values) (logging.LogQuery, error) {
	limit, err := intQuery(q, "limit", logging.DefaultLogLimit)
	if err != nil {
		return logging.LogQuery{}, err
	}
	days, err := intQuery(q, "days", 1)
	if err != nil {
		return logging.LogQuery{}, err
	}
	raw, err := boolQuery(q, "raw")
	if err != nil {
		return logging.LogQuery{}, err
	}
	query := logging.LogQuery{Limit: limit, Days: days, MinLevel: q.Get("level"), Events: q["event"], Contains: q.Get("contains"), ContentContains: q.Get("content_contains"), Raw: raw, Fields: map[string]string{}, ContentFields: map[string]string{}, ContentFieldContains: map[string][]string{}}
	if value := q.Get("since"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return query, fmt.Errorf("since must be RFC3339")
		}
		query.Since = &parsed
	}
	if value := q.Get("until"); value != "" {
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return query, fmt.Errorf("until must be RFC3339")
		}
		query.Until = &parsed
	}
	if err := addQueryPairs(query.Fields, q["field"]); err != nil {
		return query, err
	}
	if err := addQueryPairs(query.ContentFields, q["content_field"]); err != nil {
		return query, err
	}
	for _, pair := range q["content_field_contains"] {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return query, errors.New("content_field_contains must use key=value")
		}
		query.ContentFieldContains[key] = append(query.ContentFieldContains[key], value)
	}
	return query, nil
}
func addQueryPairs(target map[string]string, pairs []string) error {
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if !ok || key == "" {
			return errors.New("field filters must use key=value")
		}
		target[key] = value
	}
	return nil
}
