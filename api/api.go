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

// RunContainer запускает контейнер, собирает метрики и логи, и возвращает оба результата.
//
//	logs — это stdout+stderr контейнера.
//	statsJSON — это JSON со статистикой (DurationStr, CPUUsageNs, CPUPercent, MemUsageKB, MemLimitKB, MemPercent).
func (api *API) RunContainer(config container.ContainerConfig, showStats bool) (string, string, error) {
	// 1) Создаём контейнер
	id, err := api.containerManager.Create(config)
	if err != nil {
		return "", "", fmt.Errorf("failed to create container: %w", err)
	}
	// Удалим контейнер, когда выйдем из этой функции.
	// Размещаем defer сразу же, чтобы не забыть.
	defer api.containerManager.Remove(id)

	// 2) Стартуем контейнер и запоминаем время старта для метрик
	startTime := time.Now()
	if err := api.containerManager.Start(id); err != nil {
		return "", "", fmt.Errorf("failed to start container: %w", err)
	}

	// 3) Собираем метрики, если нужно
	var lastSummary container.ContainerStatsSummary
	var gotSummary bool
	var statsJSON string

	if showStats {
		// Канал, который закроется, когда контейнер полностью завершится
		doneCh := make(chan struct{})

		// 3a) Запустим горутину, которая ждёт сигнала о завершении контейнера
		go func() {
			// Один единственный Wait вызываем именно здесь
			api.containerManager.Wait(id)
			close(doneCh)
		}()

		// 3b) Tight‐loop polling: пока контейнер ещё “живой”, вызываем GetStatsOneShot
		for {
			// Пытаемся снять любой snapshot
			summary, err := api.containerManager.GetStatsOneShot(id, startTime)
			if err == nil {
				lastSummary = summary
				gotSummary = true
				// Как только “сырое” CPUUsageNs станет ненулевым, можно выйти из цикла
				if summary.CPUUsageNs != 0 {
					break
				}
			}
			// Проверяем, завершился ли контейнер. Если да — выходим
			select {
			case <-doneCh:
				goto AFTER_POLL
			default:
				// Иначе без задержки повторяем опрос
			}
		}

		// 3c) Мы вышли из цикла потому, что CPUUsageNs != 0, но контейнер ещё может не завершиться.
		// Нужно дождаться, пока он окончательно уйдёт (	wait до момента закрытия doneCh ).
		<-doneCh

	AFTER_POLL:
		// К этому моменту контейнер либо ушёл сам (doneCh закрылся), либо мы зашли сюда, когда doneCh уже закрыт.
		if gotSummary {
			b, _ := json.MarshalIndent(lastSummary, "", "  ")
			statsJSON = string(b)
		} else {
			statsJSON = "{}"
		}
	} else {
		// Если showStats == false, просто ждём завершения контейнера здесь
		api.containerManager.Wait(id)
	}

	// 4) Теперь, когда контейнер точно завершился, можно читать его логи.
	//
	//    Мы вызываем GetLogs **после** Wait, но до того, как defer Remove(id) физически уничтожит контейнер.
	//
	logs, err := api.containerManager.GetLogs(id)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs: %w", err)
	}

	// 5) Возвращаем сначала логи (stdout+stderr), потом JSON с метриками
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
