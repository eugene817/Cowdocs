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

// RunContainer запускает контейнер и возвращает одновременно его stdout+stderr (логи)
// и метрики (Duration, CPUPercent, MemUsage, MemLimit, MemPercent) в виде JSON-строки.
// Даже если контейнер живёт считанные миллисекунды, tight-loop гарантирует, что
// мы снимем хотя бы один summary до исчезновения cgroup.
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Обязательно удалим контейнер после того, как всё сделали
	defer api.containerManager.Remove(id)

	// 2) Стартуем контейнер и фиксируем время старта
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// Переменные для сбора метрик
	var lastSummary container.ContainerStatsSummary
	var gotSummary bool
	var statsJSON string

	if showStats {
		// Канал, который закроется, когда контейнер завершится
		doneCh := make(chan struct{})

		// 3a) Горутина, которая ждёт окончания контейнера
		go func() {
			api.containerManager.Wait(id)
			close(doneCh)
		}()

		// 3b) Tight-loop: агрессивно вызываем GetStatsOneShot(id), пока контейнер жив
		for {
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				// Сохраняем последний валидный снимок
				lastSummary = summary
				gotSummary = true
				// Если snapshot содержит ненулевые CPU или память — выходим сразу
				if summary.CPUPercent != 0 || summary.MemUsage != 0 {
					break
				}
			}
			// Проверяем, завершился ли контейнер
			select {
			case <-doneCh:
				// контейнер завершился — выходим
				goto AFTER_POLL
			default:
				// контейнер всё ещё жив — повторяем без задержки
			}
		}

	AFTER_POLL:
		// 3c) После выхода из цикла (либо мы поймали ненулевой summary, либо контейнер завершился)
		if gotSummary {
			// Маршалим в JSON
			data, _ := json.MarshalIndent(lastSummary, "", "  ")
			statsJSON = string(data)
		} else {
			// Если не удалось ни разу снять даже «нулевой» snapshot
			statsJSON = "{}"
		}
	} else {
		// Если showStats == false, просто ждём завершения контейнера
		api.containerManager.Wait(id)
	}

	// 4) После того как контейнер завершился, получаем его логи
	//    (stdout + stderr). Делаем это _после_ цикла polling, потому что
	//    мы хотим, чтобы контейнер всё ещё существовал.
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
