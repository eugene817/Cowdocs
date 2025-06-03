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

	// 2) Стартуем контейнер
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// 3) Переменные для метрик
	var lastSummary container.ContainerStatsSummary
	var gotSummary bool
	var statsJSON string

	if showStats {
		// Канал, закрывающийся после Wait
		doneCh := make(chan struct{})
		go func() {
			api.containerManager.Wait(id)
			close(doneCh)
		}()

		// Tight‐loop: пока контейнер жив, вызываем GetStatsOneShot без задержки
		for {
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				lastSummary = summary
				gotSummary = true
				// Как только сырое CPUUsageNs окажется ненулевым, выходим.
				if summary.CPUUsageNs != 0 {
					break
				}
			}
			select {
			case <-doneCh:
				// контейнер упал → выходим
				goto AFTER_POLL
			default:
				// контейнер всё ещё жив → цикл снова
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
		// showStats == false — просто ждём
		api.containerManager.Wait(id)
	}

	// 4) Логи: читаем их только **после** того как контейнер уже завершился,
	// но перед тем, как мы уйдём из функции и контейнер при этом фактически удалится.
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
