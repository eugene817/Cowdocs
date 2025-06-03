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

// RunContainer запускает контейнер и «на лету» собирает stats,
// чтобы ни один короткоживущий контейнер не ушёл с нулевыми метриками.
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// Шаг 1: Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Обязательно удаляем контейнер в defer, когда всё сделаем
	defer api.containerManager.Remove(id)

	// Шаг 2: Стартуем контейнер и фиксируем время старта
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// Переменная для хранения последнего «валидного» snapshot-а
	var lastSummary container.ContainerStatsSummary
	var gotSummary bool
	var statsJSON string

	if showStats {
		// Канал, сигнализирующий, что контейнер завершил работу
		doneCh := make(chan struct{})

		// Шаг 3a: Горутина, которая ждёт завершения контейнера
		go func() {
			api.containerManager.Wait(id)
			close(doneCh)
		}()

		// Шаг 3b: Tight-loop поллинга, чтобы поймать stats пока контейнер alive
		for {
			// Пытаемся снять одноразовый snapshot stats
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				// Если нам вернулся хоть какой-то summary, сохраняем его
				// (могут быть нулевые поля, но мы запомним последний snapshot)
				lastSummary = summary
				gotSummary = true
				// Если CPU или память уже ненулевые — останавливаемся раньше
				if summary.CPUPercent != 0 || summary.MemUsage != 0 {
					break
				}
			}
			// Проверяем, завершился ли контейнер
			select {
			case <-doneCh:
				// Контейнер уже завершился — выходим из цикла
				goto AFTER_POLL
			default:
				// Контейнер всё ещё жив — повторяем без задержки
			}
		}

	AFTER_POLL:
		// Шаг 3c: Когда контейнер вышел или мы успели получить ненулевой snapshot
		if gotSummary {
			data, _ := json.MarshalIndent(lastSummary, "", "  ")
			// У нас в lastSummary.Duration уже лежит корректный duration
			// (time.Since(startTime) мы сделали внутри GetStatsOneShot).
			statsJSON = string(data)
		} else {
			// Если ни один GetStatsOneShot не успел вернуть хоть «нулевой» snapshot
			statsJSON = "{}"
		}
	} else {
		// Если showStats == false, просто ждём завершения контейнера
		api.containerManager.Wait(id)
	}

	// Шаг 4: Берём логи контейнера (stdout+stderr) и возвращаем вместе со statsJSON
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
