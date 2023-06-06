import { DataFrameJSON } from '@grafana/data';
import { AlertQuery } from 'app/types/unified-alerting-dto';

import { arrayKeyValuesToObject } from '../utils/labels';

import { alertingApi } from './alertingApi';

export const PREVIEW_URL = 'api/v1/rule/backtest'; //we need to enable the feature flag for this
export const alertRuleApi = alertingApi.injectEndpoints({
  endpoints: (build) => ({
    preview: build.mutation<
      DataFrameJSON,
      {
        alertQueries: AlertQuery[];
        condition: string;
        customLabels: Array<{
          key: string;
          value: string;
        }>;
      }
    >({
      query: ({ alertQueries, condition, customLabels }) => ({
        url: PREVIEW_URL,
        data: {
          data: alertQueries,
          condition: condition,
          for: '0s',
          from: new Date(Date.now() - 10000).toISOString(),
          to: new Date(Date.now()).toISOString(),
          interval: '10s',
          labels: arrayKeyValuesToObject(customLabels),
          no_data_state: 'Alerting',
          title: 'testing alert for predicting potential instances',
        },
        method: 'POST',
      }),
    }),
  }),
});
