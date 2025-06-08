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
	// 1) Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	defer api.containerManager.Remove(id)

	// 2) Стартуем контейнер и запоминаем время старта
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	var statsJSON string
	if showStats {
		// 3a) Первичный снэпшот сразу после запуска
		initial, err := api.containerManager.GetStatsOneShot(id, startTime)
		if err != nil {
			return "", "", fmt.Errorf("failed to get initial stats: %w", err)
		}

		// 3b) а
		api.containerManager.Wait(id)

		// 3c) Финальный снэпшот
		final, err := api.containerManager.GetStatsOneShot(id, startTime)
		if err != nil {
			return "", "", fmt.Errorf("failed to get final stats: %w", err)
		}

		// 3d) Вычисляем дельту
		delta := container.ContainerStatsSummary{
			// DurationStr можно оставить из финального снэпшота
			DurationStr: final.DurationStr,
			CPUUsageNs:  final.CPUUsageNs - initial.CPUUsageNs,
			// CPUPercent считаем как относительный рост:
			CPUPercent: final.CPUPercent - initial.CPUPercent,
			MemUsageKB: final.MemUsageKB - initial.MemUsageKB,
			MemLimitKB: final.MemLimitKB, // лимит не меняется
			MemPercent: final.MemPercent, // процент на момент окончания
		}
		b, _ := json.MarshalIndent(delta, "", "  ")
		statsJSON = string(b)
	} else {
		// без метрик — просто ждём
		api.containerManager.Wait(id)
	}

	// 4) Получаем логи
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
