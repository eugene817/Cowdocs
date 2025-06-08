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

func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Create + defer remove
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	defer api.containerManager.Remove(id)

	// 2) Start + record startTime
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	var statsJSON string
	if showStats {
		// 3a) initial snapshot
		initial, err := api.containerManager.GetStatsOneShot(id, startTime)
		if err != nil {
			return "", "", fmt.Errorf("failed to get initial stats: %w", err)
		}

		// 3b) wait for container exit
		api.containerManager.Wait(id)
		endTime := time.Now()
		duration := endTime.Sub(startTime)

		// 3c) final snapshot (before removal!)
		final, err := api.containerManager.GetStatsOneShot(id, startTime)
		if err != nil {
			return "", "", fmt.Errorf("failed to get final stats: %w", err)
		}

		// 3d) вычисляем CPU-дельту (с защитой от underflow)
		var cpuDeltaNs uint64
		if final.CPUUsageNs >= initial.CPUUsageNs {
			cpuDeltaNs = final.CPUUsageNs - initial.CPUUsageNs
		} else {
			// если вдруг final < initial (иногда Docker возвращает 0 после stop),
			// считаем, что всё время было final
			cpuDeltaNs = final.CPUUsageNs
		}
		cpuPercent := float64(cpuDeltaNs) / float64(duration.Nanoseconds()) * 100

		// 3e) память — берём финальное значение
		memUsageKB := final.MemUsageKB
		memLimitKB := final.MemLimitKB
		memPercent := final.MemPercent

		// 3f) собираем итоговую структуру
		summary := container.ContainerStatsSummary{
			DurationStr: duration.String(),
			CPUUsageNs:  cpuDeltaNs,
			CPUPercent:  cpuPercent,
			MemUsageKB:  memUsageKB,
			MemLimitKB:  memLimitKB,
			MemPercent:  memPercent,
		}

		b, _ := json.MarshalIndent(summary, "", "  ")
		statsJSON = string(b)
	} else {
		api.containerManager.Wait(id)
	}

	// 4) лог
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %w", err)
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
