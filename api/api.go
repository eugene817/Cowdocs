package api

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"

	dockertypes "github.com/docker/docker/api/types"
	"github.com/eugene817/Cowdocs/container"
)

// API struct with containerManager field
type API struct {
	containerManager container.Manager
}

// Function to create a new API instance
func NewAPI(mgr container.Manager) *API {
	return &API{containerManager: mgr}
}

func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Create the container
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Ensure removal on exit
	defer api.containerManager.Remove(id)

	// 2) Start + record the start time
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	var (
		// We'll keep the last StatsJSON frame here
		last      dockertypes.StatsJSON
		streamErr error
		wg        sync.WaitGroup
	)

	if showStats {
		// 3a) Open the streaming stats API (Follow=false so stream ends on container exit)
		statsReader, err := api.containerManager.StreamStats(id)
		if err != nil {
			return "", "", fmt.Errorf("failed to open stats stream: %w", err)
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer statsReader.Close()
			dec := json.NewDecoder(statsReader)
			for {
				var sr dockertypes.StatsJSON
				if err := dec.Decode(&sr); err != nil {
					if err == io.EOF {
						// normal end of stream
						return
					}
					streamErr = err
					return
				}
				last = sr // save the last valid snapshot
			}
		}()
	}

	// 3b) Wait for the container to exit
	if _, err := api.containerManager.Wait(id); err != nil {
		return "", "", fmt.Errorf("failed to wait for container: %w", err)
	}

	// 3c) Wait for the stats‐stream goroutine to finish
	if showStats {
		wg.Wait()
		if streamErr != nil {
			return "", "", fmt.Errorf("error reading stats stream: %w", streamErr)
		}
	}

	// 4) Collect logs
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %w", err)
	}

	// 5) Build JSON summary if requested
	var statsJSON string
	if showStats {
		endTime := time.Now()
		duration := endTime.Sub(startTime)

		// Cumulative CPU usage in nanoseconds
		cpuNs := last.CPUStats.CPUUsage.TotalUsage
		// Average CPU% over the run
		cpuPercent := float64(cpuNs) / float64(duration.Nanoseconds()) * 100

		// Memory usage (bytes → KB)
		memUsage := last.MemoryStats.Usage
		memLimit := last.MemoryStats.Limit
		memKB := memUsage / 1024
		limitKB := memLimit / 1024

		memPercent := 0.0
		if memLimit > 0 {
			memPercent = float64(memUsage) / float64(memLimit) * 100
		}

		summary := container.ContainerStatsSummary{
			DurationStr: duration.String(),
			CPUUsageNs:  cpuNs,
			CPUPercent:  cpuPercent,
			MemUsageKB:  memKB,
			MemLimitKB:  limitKB,
			MemPercent:  memPercent,
		}

		b, _ := json.MarshalIndent(summary, "", "  ")
		statsJSON = string(b)
	}

	return logs, statsJSON, nil
}

// Function to run containers in parallel using goroutines and channels.
// The container logs are sent to the channel.
// Creates. Starts, Waits and Removes the container.
func (api *API) RunContainerParallel(config container.ContainerConfig, wg *sync.WaitGroup, c chan string) error {
	defer wg.Done()
	id, err := api.containerManager.Create(config)
	if err != nil {
		return fmt.Errorf("failed to create container: %v", err)
	}
	defer api.containerManager.Remove(id)

	if err := api.containerManager.Start(id); err != nil {
		return fmt.Errorf("failed to start container: %v", err)
	}

	if _, err := api.containerManager.Wait(id); err != nil {
		return fmt.Errorf("failed to wait for container: %v", err)
	}

	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return fmt.Errorf("failed to get logs: %v", err)
	}
	c <- logs
	return nil
}

func (api *API) EnsureImages(images []string) error {
	for _, image := range images {
		if err := api.containerManager.EnsureImage(image); err != nil {
			return fmt.Errorf("failed to ensure image %s: %v", image, err)
		}
	}
	return nil
}

func (api *API) Ping() error {
	return api.containerManager.Ping()
}
