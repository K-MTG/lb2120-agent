// Package remotewrite implements just enough of the Prometheus remote-write
// wire protocol to push a flat batch of samples to a VictoriaMetrics (or any
// remote_write compatible) endpoint. It deliberately avoids depending on the
// full github.com/prometheus/prometheus module for this: the WriteRequest
// message shape is small and has been stable for years, so it's encoded by
// hand with the low-level protowire helpers instead.
package remotewrite

import (
	"bytes"
	"fmt"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/golang/snappy"
	"google.golang.org/protobuf/encoding/protowire"
)

// Sample is one metric observation: a name, a label set (not including
// __name__), and a value, all sharing one timestamp when pushed.
type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// Client pushes batches of samples to a remote_write endpoint, merging in a
// fixed set of extra labels (e.g. job/instance) on every series.
type Client struct {
	URL         string
	ExtraLabels map[string]string
	HTTPClient  *http.Client
}

func New(url string, extraLabels map[string]string, timeout time.Duration) *Client {
	return &Client{
		URL:         url,
		ExtraLabels: extraLabels,
		HTTPClient:  &http.Client{Timeout: timeout},
	}
}

func encodeLabel(b []byte, name, value string) []byte {
	b = protowire.AppendTag(b, 1, protowire.BytesType)
	b = protowire.AppendString(b, name)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendString(b, value)
	return b
}

func encodeSample(value float64, tsMillis int64) []byte {
	var b []byte
	b = protowire.AppendTag(b, 1, protowire.Fixed64Type)
	b = protowire.AppendFixed64(b, math.Float64bits(value))
	b = protowire.AppendTag(b, 2, protowire.VarintType)
	b = protowire.AppendVarint(b, uint64(tsMillis))
	return b
}

func encodeTimeSeries(labels map[string]string, value float64, tsMillis int64) []byte {
	names := make([]string, 0, len(labels))
	for k := range labels {
		names = append(names, k)
	}
	sort.Strings(names) // remote_write requires labels sorted by name

	var b []byte
	for _, name := range names {
		var lb []byte
		lb = encodeLabel(lb, name, labels[name])
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, lb)
	}
	sb := encodeSample(value, tsMillis)
	b = protowire.AppendTag(b, 2, protowire.BytesType)
	b = protowire.AppendBytes(b, sb)
	return b
}

func encodeWriteRequest(seriesList [][]byte) []byte {
	var b []byte
	for _, s := range seriesList {
		b = protowire.AppendTag(b, 1, protowire.BytesType)
		b = protowire.AppendBytes(b, s)
	}
	return b
}

// Push encodes and sends the given samples, all at timestamp ts, as a single
// remote_write request.
func (c *Client) Push(samples []Sample, ts time.Time) error {
	tsMillis := ts.UnixMilli()
	series := make([][]byte, 0, len(samples))

	for _, s := range samples {
		labels := make(map[string]string, len(s.Labels)+len(c.ExtraLabels)+1)
		labels["__name__"] = s.Name
		for k, v := range c.ExtraLabels {
			labels[k] = v
		}
		for k, v := range s.Labels {
			labels[k] = v
		}
		series = append(series, encodeTimeSeries(labels, s.Value, tsMillis))
	}

	payload := encodeWriteRequest(series)
	compressed := snappy.Encode(nil, payload)

	req, err := http.NewRequest(http.MethodPost, c.URL, bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-protobuf")
	req.Header.Set("Content-Encoding", "snappy")
	req.Header.Set("X-Prometheus-Remote-Write-Version", "0.1.0")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("push: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("remote_write returned %s", resp.Status)
	}
	return nil
}
