// Copyright 2024 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package common

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

var (
	fsOpsMeter     = otel.Meter("fs_op")
	gcsMeter       = otel.Meter("gcs")
	fileCacheMeter = otel.Meter("file_cache")
)

type otelMetrics struct {
	fsOpsCount      metric.Int64Counter
	fsOpsErrorCount metric.Int64Counter
	fsOpsLatency    metric.Float64Histogram

	gcsReadCount          metric.Int64Counter
	gcsReadBytesCount     metric.Int64Counter
	gcsReaderCount        metric.Int64Counter
	gcsRequestCount       metric.Int64Counter
	gcsRequestLatency     metric.Float64Histogram
	gcsDownloadBytesCount metric.Int64Counter

	fileCacheReadCount      metric.Int64Counter
	fileCacheReadBytesCount metric.Int64Counter
	fileCacheReadLatency    metric.Float64Histogram
}

func (o *otelMetrics) GCSReadBytesCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.gcsReadBytesCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) GCSReaderCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.gcsReaderCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) GCSRequestCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.gcsRequestCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) GCSRequestLatency(ctx context.Context, value float64, attrs []MetricAttr) {
	o.gcsRequestLatency.Record(ctx, value, attrsToRecordOption(attrs)...)
}
func (o *otelMetrics) GCSReadCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.gcsReadCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) GCSDownloadBytesCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.gcsDownloadBytesCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}

func (o *otelMetrics) OpsCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.fsOpsCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) OpsLatency(ctx context.Context, value float64, attrs []MetricAttr) {
	o.fsOpsLatency.Record(ctx, value, attrsToRecordOption(attrs)...)
}
func (o *otelMetrics) OpsErrorCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.fsOpsErrorCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}

func (o *otelMetrics) FileCacheReadCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.fileCacheReadCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) FileCacheReadBytesCount(ctx context.Context, inc int64, attrs []MetricAttr) {
	o.fileCacheReadBytesCount.Add(ctx, inc, attrsToAddOption(attrs)...)
}
func (o *otelMetrics) FileCacheReadLatency(ctx context.Context, value float64, attrs []MetricAttr) {
	o.fileCacheReadLatency.Record(ctx, value, attrsToRecordOption(attrs)...)
}

func NewOTelMetrics() (MetricHandle, error) {
	fsOpsCount, err1 := fsOpsMeter.Int64Counter("fs/ops_count", metric.WithDescription("The number of ops processed by the file system."))
	fsOpsLatency, err2 := fsOpsMeter.Float64Histogram("fs/ops_latency", metric.WithDescription("The latency of a file system operation."), metric.WithUnit("us"),
		defaultLatencyDistribution)
	fsOpsErrorCount, err3 := fsOpsMeter.Int64Counter("fs/ops_error_count", metric.WithDescription("The number of errors generated by file system operation."))

	gcsReadCount, err4 := gcsMeter.Int64Counter("gcs/read_count", metric.WithDescription("Specifies the number of gcs reads made along with type - Sequential/Random"))
	gcsDownloadBytesCount, err5 := gcsMeter.Int64Counter("gcs/download_bytes_count",
		metric.WithDescription("The cumulative number of bytes downloaded from GCS along with type - Sequential/Random"),
		metric.WithUnit("By"))
	gcsReadBytesCount, err6 := gcsMeter.Int64Counter("gcs/read_bytes_count", metric.WithDescription("The number of bytes read from GCS objects."), metric.WithUnit("By"))
	gcsReaderCount, err7 := gcsMeter.Int64Counter("gcs/reader_count", metric.WithDescription("The number of GCS object readers opened or closed."))
	gcsRequestCount, err8 := gcsMeter.Int64Counter("gcs/request_count", metric.WithDescription("The cumulative number of GCS requests processed."))
	gcsRequestLatency, err9 := gcsMeter.Float64Histogram("gcs/request_latency", metric.WithDescription("The latency of a GCS request."), metric.WithUnit("ms"))

	fileCacheReadCount, err10 := fileCacheMeter.Int64Counter("file_cache/read_count",
		metric.WithDescription("Specifies the number of read requests made via file cache along with type - Sequential/Random and cache hit - true/false"))
	fileCacheReadBytesCount, err11 := fileCacheMeter.Int64Counter("file_cache/read_bytes_count",
		metric.WithDescription("The cumulative number of bytes read from file cache along with read type - Sequential/Random"),
		metric.WithUnit("By"))
	fileCacheReadLatency, err12 := fileCacheMeter.Float64Histogram("file_cache/read_latencies",
		metric.WithDescription("Latency of read from file cache along with cache hit - true/false"),
		metric.WithUnit("us"),
		defaultLatencyDistribution)

	if err := errors.Join(err1, err2, err3, err4, err5, err6, err7, err8, err9, err10, err11, err12); err != nil {
		return nil, err
	}
	return &otelMetrics{
		fsOpsCount:              fsOpsCount,
		fsOpsErrorCount:         fsOpsErrorCount,
		fsOpsLatency:            fsOpsLatency,
		gcsReadCount:            gcsReadCount,
		gcsReadBytesCount:       gcsReadBytesCount,
		gcsReaderCount:          gcsReaderCount,
		gcsRequestCount:         gcsRequestCount,
		gcsRequestLatency:       gcsRequestLatency,
		gcsDownloadBytesCount:   gcsDownloadBytesCount,
		fileCacheReadCount:      fileCacheReadCount,
		fileCacheReadBytesCount: fileCacheReadBytesCount,
		fileCacheReadLatency:    fileCacheReadLatency,
	}, nil

}