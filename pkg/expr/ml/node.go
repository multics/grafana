package ml

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
	jsoniter "github.com/json-iterator/go"
)

var json = jsoniter.ConfigCompatibleWithStandardLibrary

type Command interface {
	DatasourceUID() string
	Execute(ctx context.Context, from, to time.Time, executor func(path string, payload []byte) ([]byte, error)) (*backend.QueryDataResponse, error)
}

type CommandType string

const (
	Outlier CommandType = "outlier"

	// format of the time used by outlier API
	timeFormat = "2006-01-02T15:04:05.999999999"
)

type OutlierCommand struct {
	query         jsoniter.RawMessage
	datasourceUID string
}

func (c OutlierCommand) DatasourceUID() string {
	return c.datasourceUID
}

func (c OutlierCommand) Execute(ctx context.Context, from, to time.Time, execute func(path string, payload []byte) ([]byte, error)) (*backend.QueryDataResponse, error) {
	var dataMap map[string]interface{}
	// TODO rewrite to jsoniter stream
	err := json.Unmarshal(c.query, dataMap)
	if err != nil {
		return nil, err
	}
	rangeAttr, err := readValue[map[string]interface{}](dataMap, "start_end_attributes")
	if err != nil {
		return nil, err
	}
	rangeAttr["start"] = from.Format(timeFormat)
	rangeAttr["end"] = to.Format(timeFormat)
	// ???dataMap["grafana_url"] =
	body, err := json.Marshal(dataMap)
	if err != nil {
		return nil, err
	}
	responseBody, err := execute("/api/v1/outlier", body)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Status string                     `json:"status"`
		Data   *backend.QueryDataResponse `json:"dataMap,omitempty"`
		Error  string                     `json:"error,omitempty"`
	}

	err = json.Unmarshal(responseBody, &resp)
	if err != nil {
		return nil, fmt.Errorf("cannot umarshall response from plugin API: %w", err)
	}
	return resp.Data, nil
}

func UnmarshalMlCommand(query map[string]interface{}) (Command, error) {
	mlType, err := readValue[string](query, "type")
	if err != nil {
		return nil, err
	}

	d, err := readValue[map[string]interface{}](query, "data")
	if err != nil {
		return nil, err
	}

	switch strings.ToLower(mlType) {
	case string(Outlier):
		return unmarshalOutlierCommand(d)
	default:
		return nil, fmt.Errorf("unsupported command type '%v'. Supported only '%s'", mlType, Outlier)
	}
}

func readValue[T any](query map[string]interface{}, key string) (T, error) {
	var result T
	v, ok := query[key]
	if !ok {
		return result, fmt.Errorf("required field '%s' is missing", key)
	}
	result, ok = v.(T)
	if !ok {
		return result, fmt.Errorf("field '%s' has type %T but expected string", key, v)
	}
	return result, nil
}

func unmarshalOutlierCommand(data map[string]interface{}) (*OutlierCommand, error) {
	uid, err := readValue[string](data, "datasource_uid")
	if err != nil {
		return nil, err
	}

	_, err = readValue[map[string]interface{}](data, "start_end_attributes")
	if err != nil {
		return nil, err
	}

	// TODO validate the data? What if ML API changes?
	/* data is expected to be
			{
				"datasource_id": 2,
	            "datasource_uid": "prometheus",
	            "datasource_type": "prometheus",
	            "query_params": {
	                "expr": "go_goroutines",
	                "range": true,
	                "refId": "A"
	            },
				"algorithm": {
	                "name": "dbscan",
	                "config": {
	                    "epsilon": 0.2354
	                },
	                "sensitivity": 0.9
	            },
				"start_end_attributes": {
	                "interval": 60 // perhaps we can calculate it on backend using relative interval.
	            },
			}
	*/
	d, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}

	return &OutlierCommand{
		query:         d,
		datasourceUID: uid,
	}, nil
}

// type CreateOutlierAttributes struct {
// 	GrafanaURL     string `json:"grafana_url"`
// 	GrafanaAPIKey  string `json:"grafana_api_key"`
// 	DatasourceType string `json:"datasource_type"`
// 	DatasourceID   uint   `json:"datasource_id,omitempty"`
// 	DatasourceUID  string `json:"datasource_uid,omitempty"`
//
// 	// If Query is empty it should be contained in a datasource specific format
// 	// inside of QueryParms.
// 	Query       string                 `json:"query"`
// 	QueryParams map[string]interface{} `json:"query_params,omitempty"`
//
// 	StartEndAttributes QueryAttributes        `json:"start_end_attributes"`
// 	Algorithm          map[string]interface{} `json:"algorithm"`
// 	ResponseType       string                 `json:"response_type"`
//
// 	MaxSeries *uint `json:"max_series,omitempty"`
// }
//
// type CreateOutlierData struct {
// 	Type       string                  `json:"type"`
// 	Attributes CreateOutlierAttributes `json:"attributes"`
// }
//
// type CreateOutlier struct {
// 	Data CreateOutlierData `json:"data"`
// }
//
// type QueryAttributes struct {
// 	Start    Time    `json:"start"`
// 	End      Time    `json:"end"`
// 	Interval float64 `json:"interval"`
// }
//
// // Time is how python times are represented for JSON.
// type Time time.Time
//
// // ToTime converts a Time pointer to a time.Time pointer.
// func (t *Time) ToTime() *time.Time {
// 	if t == nil {
// 		return nil
// 	}
// 	time := time.Time(*t)
// 	return &time
// }
//
// const timeFormat = "2006-01-02T15:04:05.999999999"
//
// // UnmarshalJSON implements the Unmarshaler interface.
// func (t *Time) UnmarshalJSON(b []byte) error {
// 	s := strings.Trim(string(b), "\"")
// 	parsed, err := time.Parse(timeFormat, s)
// 	if err != nil {
// 		return err
// 	}
// 	*t = Time(parsed)
// 	return nil
// }
//
// // MarshalJSON implements the Marshaler interface.
// func (t Time) MarshalJSON() ([]byte, error) {
// 	return []byte("\"" + time.Time(t).Format(timeFormat) + "\""), nil
// }
//
// // ParseTime attempts to parse a string in the Python format into a new Time.
// func ParseTime(s string) (*Time, error) {
// 	parsed, err := time.Parse(timeFormat, s)
// 	if err != nil {
// 		return nil, err
// 	}
// 	t := Time(parsed)
// 	return &t, nil
// }
//
// func MustParseTime(s string) Time {
// 	t, err := ParseTime(s)
// 	if err != nil {
// 		panic(err)
// 	}
// 	return *t
// }
