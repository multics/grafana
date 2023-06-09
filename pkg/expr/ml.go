package expr

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/grafana/grafana-plugin-sdk-go/data"
	"gonum.org/v1/gonum/graph/simple"

	"github.com/grafana/grafana/pkg/api/pluginproxy"
	"github.com/grafana/grafana/pkg/api/response"
	"github.com/grafana/grafana/pkg/expr/mathexp"
	"github.com/grafana/grafana/pkg/expr/ml"
	contextmodel "github.com/grafana/grafana/pkg/services/contexthandler/model"
	"github.com/grafana/grafana/pkg/services/featuremgmt"
	"github.com/grafana/grafana/pkg/services/pluginsintegration/pluginsettings"
	"github.com/grafana/grafana/pkg/web"
)

var errMLPluginDoesNotExist = errors.New("expression type Machine Learning is not supported. Plugin 'grafana-ml-app' must be enabled")

const mlPluginID = "grafana-ml-app"

type MLNode struct {
	baseNode
	command   ml.Command
	TimeRange TimeRange
	request   *Request
}

// NodeType returns the data pipeline node type.
func (ml *MLNode) NodeType() NodeType {
	return TypeMlNode
}

// macaron unsafely asserts the http.ResponseWriter is an http.CloseNotifier, which will panic.
// Here we impl it, which will ensure this no longer happens, but neither will we take
// advantage cancelling upstream requests when the downstream has closed.
// NB: http.CloseNotifier is a deprecated ifc from before the context pkg.
// TODO Copied from alerting api/util.go
type safeMacaronWrapper struct {
	http.ResponseWriter
}

func (w *safeMacaronWrapper) CloseNotify() <-chan bool {
	return make(chan bool)
}

// Execute runs the node and adds the results to vars. If the node requires
// other nodes they must have already been executed and their results must
// already by in vars.
func (ml *MLNode) Execute(ctx context.Context, now time.Time, _ mathexp.Vars, s *Service) (r mathexp.Results, e error) {
	var result mathexp.Results
	timeRange := ml.TimeRange.AbsoluteTime(now)
	plugin, exist := s.plugins.Plugin(ctx, mlPluginID)
	if !exist {
		return result, errMLPluginDoesNotExist
	}

	dto, err := s.pluginSettings.GetPluginSettingByPluginID(ctx, &pluginsettings.GetByPluginIDArgs{
		PluginID: mlPluginID,
		OrgID:    ml.request.OrgId,
	})
	if err != nil {
		return result, QueryError{
			RefID: ml.refID,
			Err:   fmt.Errorf("cannot query plugin: %w", err),
		}
	}

	// copied from plugin_proxy.go
	pluginProxyTransport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: s.cfg.PluginsAppsSkipVerifyTLS,
			Renegotiation:      tls.RenegotiateFreelyAsClient,
		},
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
	}

	resp, err := ml.command.Execute(ctx, timeRange.From, timeRange.To, func(path string, payload []byte) ([]byte, error) {

		req, err := http.NewRequest(http.MethodPost, path, bytes.NewBuffer(payload))
		if err != nil {
			return nil, err
		}
		resp := response.CreateNormalResponse(make(http.Header), nil, 0)
		reqctx := &contextmodel.ReqContext{
			Context: &web.Context{
				Req:  req,
				Resp: web.NewResponseWriter(req.Method, &safeMacaronWrapper{resp}),
			},
			SignedInUser: ml.request.User,
		}
		proxy, err := pluginproxy.NewPluginProxy(dto, plugin.Routes, reqctx, path, s.cfg, s.secretService, s.tracer, pluginProxyTransport)
		if err != nil {
			return nil, err
		}
		proxy.HandleRequest()

		if resp.Status() >= 200 && resp.Status() < 300 {
			return resp.Body(), nil
		}
		return nil, fmt.Errorf("failed to send a POST request to plugin %s via proxy by path %s, status code: %v, msg:%s", mlPluginID, path, resp.Status(), resp.Body())
	})
	if err != nil {
		return result, QueryError{
			RefID: ml.refID,
			Err:   err,
		}
	}

	responseType := "unknown"
	respStatus := "success"
	var useDataplane bool
	defer func() {
		if e != nil {
			responseType = "error"
			respStatus = "failure"
		}
		logger.Debug("ML plugin queried", "responseType", responseType)

		s.metrics.dsRequests.WithLabelValues(respStatus, fmt.Sprintf("%t", useDataplane)).Inc()
	}()

	vals := make([]mathexp.Value, 0)
	response, ok := resp.Responses[ml.refID]
	if !ok {
		if len(resp.Responses) > 0 {
			keys := make([]string, 0, len(resp.Responses))
			for refID := range resp.Responses {
				keys = append(keys, refID)
			}
			logger.Warn("Can't find response by refID. Return nodata", "responseRefIds", keys)
		}
		return mathexp.Results{Values: mathexp.Values{mathexp.NoData{}.New()}}, nil
	}

	if response.Error != nil {
		return mathexp.Results{}, QueryError{RefID: ml.refID, Err: response.Error}
	}

	var dt data.FrameType
	dt, useDataplane, _ = shouldUseDataplane(response.Frames, logger, s.features.IsEnabled(featuremgmt.FlagDisableSSEDataplane))
	if useDataplane {
		logger.Debug("Handling SSE data source query through dataplane", "datatype", dt)
		return handleDataplaneFrames(ctx, s.tracer, dt, response.Frames)
	}

	// TODO change
	if isAllFrameVectors("prometheus", response.Frames) { // Prometheus Specific Handling
		vals, err = framesToNumbers(response.Frames)
		if err != nil {
			return mathexp.Results{}, fmt.Errorf("failed to read frames as numbers: %w", err)
		}
		responseType = "vector"
		return mathexp.Results{Values: vals}, nil
	}

	if len(response.Frames) == 1 {
		frame := response.Frames[0]
		// Handle Untyped NoData
		if len(frame.Fields) == 0 {
			return mathexp.Results{Values: mathexp.Values{mathexp.NoData{Frame: frame}}}, nil
		}

		// Handle Numeric Table
		if frame.TimeSeriesSchema().Type == data.TimeSeriesTypeNot && isNumberTable(frame) {
			numberSet, err := extractNumberSet(frame)
			if err != nil {
				return mathexp.Results{}, err
			}
			for _, n := range numberSet {
				vals = append(vals, n)
			}
			responseType = "number set"
			return mathexp.Results{
				Values: vals,
			}, nil
		}
	}

	for _, frame := range response.Frames {
		// // Check for TimeSeriesTypeNot in InfluxDB queries. A data frame of this type will cause
		// // the WideToMany() function to error out, which results in unhealthy alerts.
		// // This check should be removed once inconsistencies in data source responses are solved.
		// if frame.TimeSeriesSchema().Type == data.TimeSeriesTypeNot && dataSource == datasources.DS_INFLUXDB {
		// 	logger.Warn("Ignoring InfluxDB data frame due to missing numeric fields")
		// 	continue
		// }
		series, err := WideToMany(frame)
		if err != nil {
			return mathexp.Results{}, err
		}
		for _, s := range series {
			vals = append(vals, s)
		}
	}

	responseType = "series set"
	return mathexp.Results{
		Values: vals, // TODO vals can be empty. Should we replace with no-data?
	}, nil
}

func (s *Service) buildMlNode(dp *simple.DirectedGraph, rn *rawNode, req *Request) (Node, error) {
	if _, exist := s.plugins.Plugin(context.TODO(), mlPluginID); !exist {
		return nil, errMLPluginDoesNotExist
	}
	if rn.TimeRange == nil {
		return nil, errors.New("time range must be specified")
	}
	cmd, err := ml.UnmarshalMlCommand(rn.Query)
	if err != nil {
		return nil, err
	}

	return &MLNode{
		baseNode: baseNode{
			id:    dp.NewNode().ID(),
			refID: rn.RefID,
		},
		TimeRange: rn.TimeRange,
		command:   cmd,
		request:   req,
	}, nil
}
