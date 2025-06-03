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

// RunContainer запускает контейнер, собирает логи и метрики и возвращает оба результата.
// logs — это stdout+stderr контейнера.
// statsJSON — это JSON со статистикой (DurationStr, CPUUsageNs, CPUPercent, MemUsageKB, MemLimitKB, MemPercent).
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Create
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Будем удалять контейнер только после того, как прочитаем логи и stats.

	// 2) Start
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// 3) Собираем статистику, если нужно
	var lastSummary container.ContainerStatsSummary
	var gotSummary bool
	var statsJSON string

	if showStats {
		doneCh := make(chan struct{})
		// 3a) Горутина, которая ждет, пока контейнер завершится
		go func() {
			api.containerManager.Wait(id)
			close(doneCh)
		}()

		// 3b) Tight‐loop polling, пока контейнер еще alive
		for {
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				lastSummary = summary
				gotSummary = true
				// Проверяем “сырую” CPUUsageNs: как только она станет ненулевой, завершаем опрос
				if summary.CPUUsageNs != 0 {
					break
				}
			}
			select {
			case <-doneCh:
				// контейнер завершился, выходим из цикла
				goto AFTER_POLL
			default:
				// контейнер все еще работает, идем дальше в цикле без sleep
			}
		}
	AFTER_POLL:
		if gotSummary {
			b, _ := json.MarshalIndent(lastSummary, "", "  ")
			statsJSON = string(b)
		} else {
			statsJSON = "{}"
		}
	} else {
		// Если showStats == false, просто ждем завершения контейнера
		api.containerManager.Wait(id)
	}

	// 4) Читаем логи **после** Wait, но **до** удаления контейнера
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %w", err)
	}

	defer api.containerManager.Remove(id)

	// 5) Возвращаем: сначала логи, потом statsJSON
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
