package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"time"
)

func syncStartTime(ctx context.Context, srcClient, dstClient *Client) (time.Time, error) {
	recordTime, err := dstClient.LastRecordTime(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to get last record time of destination: %w", err)
	}

	// fallback to start time of source if no data in destination
	if recordTime.IsZero() {
		recordTime, err = srcClient.FirstRecordTime(ctx)
		if err != nil {
			return time.Time{}, fmt.Errorf("failed to get first record time of source: %w", err)
		}
	}

	recordTime = recordTime.UTC()
	return time.Date(
		recordTime.Year(),
		recordTime.Month(),
		recordTime.Day(),
		recordTime.Hour(),
		0,
		0,
		0,
		recordTime.Location()), nil
}

func syncInflux(ctx context.Context, srcClient, dstClient *Client) error {
	var counter uint64
	start := time.Now()

	// get start time for sync
	startTime, err := syncStartTime(ctx, srcClient, dstClient)
	if err != nil {
		return fmt.Errorf("failed to get start time: %w", err)
	}

	for currentTime := startTime; currentTime.Before(time.Now()); currentTime = currentTime.Add(time.Hour) {
		log.Printf("sync: %s", currentTime.Format(time.DateTime))
		points, err := srcClient.QueryPoints(ctx, currentTime, currentTime.Add(time.Hour))
		if err != nil {
			return fmt.Errorf("failed to query points from source: %w", err)
		}

		log.Printf("> write %d", len(points))
		err = dstClient.WritePoints(points)
		if err != nil {
			return fmt.Errorf("failed to write to destination: %w", err)
		}
		counter += uint64(len(points))
	}
	log.Printf("imported %d points in %s", counter, time.Since(start))
	return nil
}

func createClient(prefix string) *Client {
	return NewClient(
		os.Getenv(prefix+"_URL"),
		os.Getenv(prefix+"_TOKEN"),
		os.Getenv(prefix+"_ORG"),
		os.Getenv(prefix+"_BUCKET"))
}

var wg sync.WaitGroup

func doSyncInflux(ctx context.Context, srcClient, dstClient *Client) {
	wg.Add(1)
	defer wg.Done()

	err := syncInflux(ctx, srcClient, dstClient)
	if err != nil {
		log.Printf("ERROR: %v", err)
	}
}

func main() {
	updateInterval, err := time.ParseDuration(os.Getenv("INFLUXDB_SYNC_INTERVAL"))
	if err != nil {
		panic(err)
	}

	srcClient := createClient("INFLUXDB_SRC")
	dstClient := createClient("INFLUXDB_DST")

	defer srcClient.Close()
	defer dstClient.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt)
		<-c

		cancel()
	}()

	log.Print("run initial sync")
	doSyncInflux(ctx, srcClient, dstClient)

	log.Printf("start scheduled syn every %s", updateInterval)
	ticker := time.NewTicker(updateInterval)
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			doSyncInflux(ctx, srcClient, dstClient)
		}
	}

	log.Print("application stopped")
	wg.Wait()
}
