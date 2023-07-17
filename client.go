package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	"github.com/influxdata/influxdb-client-go/v2/api"
	"github.com/influxdata/influxdb-client-go/v2/api/query"
	"github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/spf13/cast"
)

var ErrNoResult = errors.New("no records received")

type Client struct {
	client influxdb2.Client

	bucket string

	queryApi api.QueryAPI
	writeApi api.WriteAPI
}

func NewClient(url, token, org, bucket string) *Client {
	client := influxdb2.NewClient(url, token)

	return &Client{
		client:   client,
		bucket:   bucket,
		queryApi: client.QueryAPI(org),
		writeApi: client.WriteAPI(org, bucket),
	}
}

func (c *Client) queryRow(ctx context.Context, q string) (*query.FluxRecord, error) {
	result, err := c.queryApi.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("failed execute query: %w", err)
	}

	if !result.Next() {
		return nil, ErrNoResult
	}

	return result.Record(), nil
}

func (c *Client) Close() {
	c.client.Close()
}

func (c *Client) FirstRecordTime(ctx context.Context) (time.Time, error) {
	record, err := c.queryRow(ctx, fmt.Sprintf(`
from(bucket:"%s")
  |> range(start: 0)
  |> first()
  |> keep(columns:["_time"])
  |> set(key: "_value", value: "0")
  |> sort(columns: ["_time"])
  |> first()`, c.bucket))
	if err != nil {
		if err == ErrNoResult {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("failed to request record: %w", err)
	}

	return record.Time(), nil
}

func (c *Client) LastRecordTime(ctx context.Context) (time.Time, error) {
	record, err := c.queryRow(ctx, fmt.Sprintf(`
from(bucket:"%s")
  |> range(start: 0)
  |> last()
  |> keep(columns:["_time"])
  |> set(key: "_value", value: "0")
  |> sort(columns: ["_time"])
  |> last()`, c.bucket))
	if err != nil {
		if err == ErrNoResult {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("failed to request record: %w", err)
	}

	return record.Time(), nil
}

func recordToPoint(record *query.FluxRecord) *write.Point {
	tags := make(map[string]string)
	for key, value := range record.Values() {
		if key == "result" || key == "table" || strings.HasPrefix(key, "_") {
			continue
		}
		tags[key] = cast.ToString(value)
	}

	return write.NewPoint(record.Measurement(), tags, map[string]interface{}{
		record.Field(): record.Value(),
	}, record.Time())
}

func (c *Client) QueryPoints(ctx context.Context, start, stop time.Time) ([]*write.Point, error) {
	result, err := c.queryApi.Query(ctx, fmt.Sprintf(`
	from(bucket:"%s")
	  |> range(start: %s, stop: %s)
	  |> drop(columns: ["_start", "_stop"])
	  |> sort(columns: ["_time"])`,
		c.bucket, start.Format(time.RFC3339), stop.Format(time.RFC3339)))
	if err != nil {
		return nil, fmt.Errorf("failed execute query: %w", err)
	}

	var points []*write.Point
	for result.Next() {
		points = append(points, recordToPoint(result.Record()))
	}
	if result.Err() != nil {
		return nil, fmt.Errorf("failed parse query response: %w", result.Err())
	}
	return points, nil
}

func (c *Client) WritePoints(points []*write.Point) error {
	var errList []error
	cancelCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		errs := c.writeApi.Errors()
		for {
			select {
			case err := <-errs:
				errList = append(errList, err)
			case <-cancelCtx.Done():
				return
			}
		}
	}()

	for _, point := range points {
		c.writeApi.WritePoint(point)
	}
	c.writeApi.Flush()
	cancel()
	wg.Wait()

	if len(errList) > 0 {
		return fmt.Errorf("%d errors while writing points - first: %w", len(errList), errList[0])
	}
	return nil
}
