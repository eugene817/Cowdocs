package api

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

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

// RunContainer creates, starts, waits for, and removes a container.
// If showStats is true, it also collects final stats (peak memory, last memory, last CPU%, and duration)
// via GetStatsStreamed, and returns them as a JSON string alongside the logs.
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1. Create the container (but do not start it yet).
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %v", err)
	}
	// Ensure removal at the end
	defer api.containerManager.Remove(id)

	// Prepare channels for stats summary or error, if needed
	var (
		statsCh    chan container.ContainerStatsSummary
		statsErrCh chan error
	)
	if showStats {
		statsCh = make(chan container.ContainerStatsSummary, 1)
		statsErrCh = make(chan error, 1)

		// 2. Launch a goroutine that streams stats until container exit.
		//    We pass the container ID, the time we start streaming, and the channels.
		go api.containerManager.GetStatsStreamed(id, time.Now(), statsCh, statsErrCh)
	}

	// 3. Start the container.
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %v", err)
	}

	// 4. Wait for container to exit.
	if _, err := api.containerManager.Wait(id); err != nil {
		return "", "", fmt.Errorf("failed to wait for container: %v", err)
	}

	// 5. Retrieve logs.
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %v", err)
	}

	// 6. If stats were requested, collect the summary or error (with a short timeout).
	if showStats {
		select {
		case summary := <-statsCh:
			// Marshal the ContainerStatsSummary into JSON for return
			summaryBytes, err := json.MarshalIndent(summary, "", "  ")
			if err != nil {
				return logs, "", fmt.Errorf("failed to serialize stats summary: %v", err)
			}
			return logs, string(summaryBytes), nil

		case errStat := <-statsErrCh:
			return logs, "", fmt.Errorf("error streaming stats: %v", errStat)

		case <-time.After(1 * time.Second):
			// In case GetStatsStreamed didn't send anything (unlikely unless error), return logs anyway
			return logs, "", fmt.Errorf("timeout waiting for stats summary")
		}
	}

	// 7. If showStats is false, just return logs and empty stats.
	return logs, "", nil
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
